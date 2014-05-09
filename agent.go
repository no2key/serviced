// Copyright 2014, The Serviced Authors. All rights reserved.
// Use of this source code is governed by a
// license that can be found in the LICENSE file.

// Package serviced - agent implements a service that runs on a serviced node.
// It is responsible for ensuring that a particular node is running the correct
// services and reporting the state and health of those services back to the
// master serviced.
package serviced

import (
	"regexp"

	"github.com/zenoss/glog"
	"github.com/zenoss/serviced/commons"
	coordclient "github.com/zenoss/serviced/coordinator/client"
	coordzk "github.com/zenoss/serviced/coordinator/client/zookeeper"
	"github.com/zenoss/serviced/dao"
	"github.com/zenoss/serviced/domain"
	"github.com/zenoss/serviced/domain/service"
	"github.com/zenoss/serviced/domain/servicestate"
	"github.com/zenoss/serviced/proxy"
	"github.com/zenoss/serviced/utils"
	"github.com/zenoss/serviced/volume"
	"github.com/zenoss/serviced/zzk"

	docker "github.com/zenoss/go-dockerclient"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

/*
 glog levels:
 0: important info that should always be shown
 1: info that might be important for debugging
 2: very verbose debug info
 3: trace level info
*/

const (
	dockerEndpoint     = "unix:///var/run/docker.sock"
	circularBufferSize = 1000
)

// HostAgent is an instance of the control plane Agent.
type HostAgent struct {
	master          string               // the connection string to the master agent
	uiport          string               // the port to the ui (legacy was port 8787, now default 443)
	hostID          string               // the hostID of the current host
	dockerDNS       []string             // docker dns addresses
	varPath         string               // directory to store serviced	 data
	mount           []string             // each element is in the form: dockerImage,hostPath,containerPath
	vfs             string               // driver for container volumes
	currentServices map[string]*exec.Cmd // the current running services
	mux             proxy.TCPMux
	closing         chan chan error
	proxyRegistry   proxy.ProxyRegistry
	zkClient        *coordclient.Client
	dockerRegistry  string // the docker registry to use
}

// assert that this implemenents the Agent interface

func getZkDSN(zookeepers []string) string {
	if len(zookeepers) == 0 {
		zookeepers = []string{"127.0.0.1:2181"}
	}
	dsn := coordzk.DSN{
		Servers: zookeepers,
		Timeout: time.Second * 15,
	}
	return dsn.String()
}

// NewHostAgent creates a new HostAgent given a connection string
func NewHostAgent(master string, uiport string, dockerDNS []string, varPath string, mount []string, vfs string, zookeepers []string, mux proxy.TCPMux, dockerRegistry string) (*HostAgent, error) {
	// save off the arguments
	agent := &HostAgent{}
	agent.dockerRegistry = dockerRegistry
	agent.master = master
	agent.uiport = uiport
	agent.dockerDNS = dockerDNS
	agent.varPath = varPath
	agent.mount = mount
	agent.vfs = vfs
	agent.mux = mux
	if agent.mux.Enabled {
		go agent.mux.ListenAndMux()
	}

	dsn := getZkDSN(zookeepers)
	basePath := ""
	zkClient, err := coordclient.New("zookeeper", dsn, basePath, nil)
	if err != nil {
		return nil, err
	}
	agent.zkClient = zkClient

	agent.closing = make(chan chan error)
	hostID, err := utils.HostID()
	if err != nil {
		panic("Could not get hostid")
	}
	agent.hostID = hostID
	agent.currentServices = make(map[string]*exec.Cmd)

	agent.proxyRegistry = proxy.NewDefaultProxyRegistry()
	go agent.start()
	return agent, err

	/* FIXME: this should work here

	addr, err := net.ResolveTCPAddr("tcp", processForwarderAddr)
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}

	sio := shell.NewProcessForwarderServer(proxyOptions.servicedEndpoint)
	sio.Handle("/", http.FileServer(http.Dir("/serviced/www/")))
	go http.Serve(listener, sio)
	c := &ControllerP{
		processForwarderListener: listener,
	}
	*/

}

// Use the Context field of the given template to fill in all the templates in
// the Command fields of the template's ServiceDefinitions
func injectContext(s *service.Service, cp dao.ControlPlane) error {

	getSvc := func(svcID string) (service.Service, error) {
		svc := service.Service{}
		err := cp.GetService(svcID, &svc)
		return svc, err
	}
	err := s.EvaluateLogConfigTemplate(getSvc)
	if err != nil {
		return err
	}
	return s.EvaluateStartupTemplate(getSvc)
}

// Shutdown stops the agent
func (a *HostAgent) Shutdown() error {
	glog.V(2).Info("Issuing shutdown signal")
	errc := make(chan error)
	a.closing <- errc
	glog.Info("exiting shutdown")
	return <-errc
}

// Attempts to attach to a running container
func (a *HostAgent) attachToService(conn coordclient.Connection, procFinished chan<- int, serviceState *servicestate.ServiceState, hss *zzk.HostServiceState) (bool, error) {

	// get docker status
	containerState, err := getDockerState(serviceState.DockerId)
	glog.V(2).Infof("Agent.updateCurrentState got container state for docker ID %s: %v", serviceState.DockerId, containerState)

	switch {
	case err == nil && !containerState.State.Running:
		glog.V(1).Infof("Container does not appear to be running: %s", serviceState.Id)
		return false, errors.New("Container not running for " + serviceState.Id)

	case err != nil && strings.HasPrefix(err.Error(), "no container"):
		glog.Warningf("Error retrieving container state: %s", serviceState.Id)
		return false, err

	}

	dc, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		glog.Errorf("can't create docker client: %v", err)
		return false, err
	}

	go a.waitForProcessToDie(dc, conn, serviceState.DockerId, procFinished, serviceState)
	return true, nil
}

func markTerminated(conn coordclient.Connection, hss *zzk.HostServiceState) {
	ssPath := zzk.ServiceStatePath(hss.ServiceId, hss.ServiceStateId)
	exists, err := conn.Exists(ssPath)
	if err != nil {
		glog.V(0).Infof("Unable to get service state %s for delete because: %v", ssPath, err)
		return
	}
	if exists {
		err = conn.Delete(ssPath)
		if err != nil {
			glog.V(0).Infof("Unable to delete service state %s because: %v", ssPath, err)
			return
		}
	}

	hssPath := zzk.HostServiceStatePath(hss.HostId, hss.ServiceStateId)
	exists, err = conn.Exists(hssPath)
	if err != nil {
		glog.V(0).Infof("Unable to get host service state %s for delete becaus: %v", hssPath, err)
		return
	}
	if exists {
		err = conn.Delete(hssPath)
		if err != nil {
			glog.V(0).Infof("Unable to delete host service state %s", hssPath)
		}

	}
	return
}

// Terminate a particular service instance (serviceState) on the localhost.
func (a *HostAgent) terminateInstance(conn coordclient.Connection, serviceState *servicestate.ServiceState) error {
	err := a.dockerTerminate(serviceState.Id)
	if err != nil {
		return err
	}
	markTerminated(conn, zzk.SsToHss(serviceState))
	return nil
}

func (a *HostAgent) terminateAttached(conn coordclient.Connection, procFinished <-chan int, ss *servicestate.ServiceState) error {
	err := a.dockerTerminate(ss.Id)
	if err != nil {
		return err
	}
	<-procFinished
	markTerminated(conn, zzk.SsToHss(ss))
	return nil
}

func (a *HostAgent) dockerRemove(dockerID string) error {
	glog.V(1).Infof("Ensuring that container %s does not exist", dockerID)

	dc, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		glog.Errorf("can't create docker client: %v", err)
		return err
	}

	if err = dc.RemoveContainer(docker.RemoveContainerOptions{dockerID, true}); err != nil {
		glog.Errorf("unable to remove container %s: %v", dockerID, err)
		return err
	}

	glog.V(2).Infof("Successfully removed %s", dockerID)
	return nil
}

func (a *HostAgent) dockerTerminate(dockerID string) error {
	glog.V(1).Infof("Killing container %s", dockerID)

	dc, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		glog.Errorf("can't create docker client: %v", err)
		return err
	}

	if err = dc.KillContainer(dockerID); err != nil && !strings.Contains(err.Error(), "No such container") {
		glog.Errorf("unable to kill container %s: %v", dockerID, err)
		return err
	}

	glog.V(2).Infof("Successfully killed %s", dockerID)
	return nil
}

// Get the state of the docker container given the dockerId
func getDockerState(dockerID string) (*docker.Container, error) {
	glog.V(1).Infof("Inspecting container: %s", dockerID)

	dc, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		glog.Errorf("can't create docker client: %v", err)
		return nil, err
	}

	return dc.InspectContainer(dockerID)
}

func dumpOut(stdout, stderr io.Reader, size int) {
	dumpBuffer(stdout, size, "stdout")
	dumpBuffer(stderr, size, "stderr")
}

func dumpBuffer(reader io.Reader, size int, name string) {
	buffer := make([]byte, size)
	if n, err := reader.Read(buffer); err != nil {
		glog.V(1).Infof("Unable to read %s of dump", name)
	} else {
		message := strings.TrimSpace(string(buffer[:n]))
		if len(message) > 0 {
			glog.V(0).Infof("Process %s:\n%s", name, message)
		}
	}
}

func (a *HostAgent) waitForProcessToDie(dc *docker.Client, conn coordclient.Connection, containerID string, procFinished chan<- int, serviceState *servicestate.ServiceState) {
	defer func() {
		procFinished <- 1
	}()

	exited := make(chan error)

	go func() {
		rc, err := dc.WaitContainer(containerID)
		if err != nil {
			glog.Errorf("docker wait exited with: %v : %d", err, rc)
			// TODO: output of docker logs is potentially very large
			// this should be implemented another way, perhaps a docker attach
			// or extend docker to give the last N seconds
			cmd := exec.Command("docker", "logs", containerID)
			output, err := cmd.CombinedOutput()
			if err != nil {
				glog.Errorf("Could not get logs for container %s", containerID)
			} else {
				// get last 1000 bytes
				str := string(output)
				last := len(str) - 1000
				if last < 0 {
					last = 0
				}
				str = str[last:]
				glog.Warning("Last 1000 bytes of container %s: %s", containerID, str)

			}

		}
		glog.Infof("docker wait %s exited", containerID)
		// get rid of the container
		if rmErr := dc.RemoveContainer(docker.RemoveContainerOptions{ID: containerID, RemoveVolumes: true}); rmErr != nil {
			glog.Errorf("Could not remove container: %s: %s", containerID, rmErr)
		}
		exited <- err
	}()

	// We are name the container the same as its service state ID, so use that as an alias
	dockerID := serviceState.Id
	serviceState.DockerId = dockerID

	time.Sleep(1 * time.Second) // Sleep to give docker a chance to start

	var ctr *docker.Container
	var err error
	for i := 0; i < 30; i++ {
		if ctr, err = getDockerState(dockerID); err != nil {
			time.Sleep(3 * time.Second) // Sleep to give docker a chance to start
			glog.V(2).Infof("Problem getting service state for %s :%v", serviceState.Id, err)
		} else {
			break
		}
	}

	if err != nil {
		return
		//TODO: should	"cmd" be cleaned up before returning?
	}

	var sState *servicestate.ServiceState
	if err = zzk.LoadAndUpdateServiceState(conn, serviceState.ServiceId, serviceState.Id, func(ss *servicestate.ServiceState) {
		ss.DockerId = ctr.ID
		ss.Started = time.Now()
		ss.Terminated = time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
		ss.PrivateIp = ctr.NetworkSettings.IPAddress
		ss.PortMapping = make(map[string][]domain.HostIpAndPort)
		for k, v := range ctr.NetworkSettings.Ports {
			pm := []domain.HostIpAndPort{}
			for _, pb := range v {
				pm = append(pm, domain.HostIpAndPort{pb.HostIp, pb.HostPort})
			}
			ss.PortMapping[string(k)] = pm
		}
		sState = ss
	}); err != nil {
		glog.Warningf("Unable to update service state %s: %v", serviceState.Id, err)
		//TODO: should	"cmd" be cleaned up before returning?
	} else {

		//start IP resource proxy for each endpoint
		var service service.Service
		if err = zzk.LoadService(conn, serviceState.ServiceId, &service); err != nil {
			glog.Warningf("Unable to read service %s: %v", serviceState.Id, err)
		} else {
			glog.V(4).Infof("Looking for address assignment in service %s:%s", service.Name, service.Id)
			for _, endpoint := range service.Endpoints {
				if addressConfig := endpoint.GetAssignment(); addressConfig != nil {
					glog.V(4).Infof("Found address assignment for %s:%s endpoint %s", service.Name, service.Id, endpoint.Name)
					proxyID := fmt.Sprintf("%v:%v", sState.ServiceId, endpoint.Name)

					frontEnd := proxy.ProxyAddress{IP: addressConfig.IPAddr, Port: addressConfig.Port}
					backEnd := proxy.ProxyAddress{IP: sState.PrivateIp, Port: endpoint.PortNumber}

					err = a.proxyRegistry.CreateProxy(proxyID, endpoint.Protocol, frontEnd, backEnd)
					if err != nil {
						glog.Warningf("Could not start External address proxy for %v; error: proxyId", proxyID, err)
					}
					defer a.proxyRegistry.RemoveProxy(proxyID)

				}
			}

		}

		glog.V(1).Infof("SSPath: %s, PortMapping: %v", zzk.ServiceStatePath(serviceState.ServiceId, serviceState.Id), serviceState.PortMapping)

		loop := true
		stateUpdateEvery := time.Tick(time.Second * 20)
		for loop {
			select {
			case err := <-exited:
				if err != nil {
					if exiterr, ok := err.(*exec.ExitError); ok {
						if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
							statusCode := status.ExitStatus()
							switch {
							case statusCode == 137:
								glog.V(1).Infof("Docker process killed: %s", serviceState.Id)

							case statusCode == 2:
								glog.V(1).Infof("Docker process stopped: %s", serviceState.Id)

							default:
								glog.V(0).Infof("Docker process %s exited with code %d", serviceState.Id, statusCode)
							}
						}
					} else {
						glog.V(1).Info("Unable to determine exit code for %s", serviceState.Id)
					}
				} else {
					glog.V(0).Infof("Process for service state %s finished", serviceState.Id)
				}
				loop = false
			case <-stateUpdateEvery:
				containerState, err := getDockerState(containerID)
				if err != nil {
					glog.Errorf("Could not get docker state: %v", err)
					continue
				}
				if err = zzk.LoadAndUpdateServiceState(conn, serviceState.ServiceId, serviceState.Id, func(ss *servicestate.ServiceState) {
					ss.DockerId = containerState.ID
					ss.Started = time.Now()
					ss.Terminated = time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
					ss.PrivateIp = containerState.NetworkSettings.IPAddress
					ss.PortMapping = make(map[string][]domain.HostIpAndPort)
					for k, v := range ctr.NetworkSettings.Ports {
						pm := []domain.HostIpAndPort{}
						for _, pb := range v {
							pm = append(pm, domain.HostIpAndPort{pb.HostIp, pb.HostPort})
						}
						ss.PortMapping[string(k)] = pm
					}
					sState = ss
				}); err != nil {
					glog.Warningf("Unable to update service state %s: %v", serviceState.Id, err)
				}

			}
		}
		if err = zzk.ResetServiceState(conn, serviceState.ServiceId, serviceState.Id); err != nil {
			glog.Errorf("Caught error marking process termination time for %s: %v", serviceState.Id, err)
		}

	}
}

func getSubvolume(varPath, poolID, tenantID, fs string) (*volume.Volume, error) {
	baseDir, _ := filepath.Abs(path.Join(varPath, "volumes"))
	if _, err := volume.Mount(fs, poolID, baseDir); err != nil {
		return nil, err
	}
	baseDir, _ = filepath.Abs(path.Join(varPath, "volumes", poolID))
	return volume.Mount(fs, tenantID, baseDir)
}

/*
writeConfFile is responsible for writing contents out to a file
Input string prefix	 : cp_cd67c62b-e462-5137-2cd8-38732db4abd9_zenmodeler_logstash_forwarder_conf_
Input string id		 : Service ID (example cd67c62b-e462-5137-2cd8-38732db4abd9)
Input string filename: zenmodeler_logstash_forwarder_conf
Input string content : the content that you wish to write to a file
Output *os.File	 f	 : file handler to the file that you've just opened and written the content to
Example name of file that is written: /tmp/cp_cd67c62b-e462-5137-2cd8-38732db4abd9_zenmodeler_logstash_forwarder_conf_592084261
*/
func writeConfFile(prefix string, id string, filename string, content string) (*os.File, error) {
	f, err := ioutil.TempFile("", prefix)
	if err != nil {
		glog.Errorf("Could not generate tempfile for config %s %s", id, filename)
		return f, err
	}
	_, err = f.WriteString(content)
	if err != nil {
		glog.Errorf("Could not write out config file %s %s", id, filename)
		return f, err
	}

	return f, nil
}

// chownConfFile() runs 'chown $owner $filename && chmod $permissions $filename'
// using the given dockerImage. An error is returned if owner is not specified,
// the owner is not in user:group format, or if there was a problem setting
// the permissions.
func chownConfFile(filename, owner, permissions string, dockerImage string) error {
	// TODO: reach in to the dockerImage and get the effective UID, GID so we can do this without a bind mount
	if !validOwnerSpec(owner) {
		return fmt.Errorf("unsupported owner specification: %s", owner)
	}

	uid, gid, err := getInternalImageIds(owner, dockerImage)
	if err != nil {
		return err
	}
	// this will fail if we are not running as root
	if err := os.Chown(filename, uid, gid); err != nil {
		return err
	}
	octal, err := strconv.ParseInt(permissions, 8, 32)
	if err != nil {
		return err
	}
	if err := os.Chmod(filename, os.FileMode(octal)); err != nil {
		return err
	}
	return nil
}

// startService starts a new instance of the specified service and updates the control plane state accordingly.
func (a *HostAgent) startService(conn coordclient.Connection, procFinished chan<- int, service *service.Service, serviceState *servicestate.ServiceState) (bool, error) {
	glog.V(2).Infof("About to start service %s with name %s", service.Id, service.Name)
	client, err := NewControlClient(a.master)
	if err != nil {
		glog.Errorf("Could not start ControlPlane client %v", err)
		return false, err
	}
	defer client.Close()

	// start from a known good state
	a.dockerTerminate(serviceState.Id)
	a.dockerRemove(serviceState.Id)

	dc, err := docker.NewClient(dockerEndpoint)
	if err != nil {
		glog.Errorf("can't create Docker client: %v ", err)
		return false, err
	}

	em, err := dc.MonitorEvents()
	if err != nil {
		glog.Errorf("can't monitor Docker events: %v", err)
		return false, err
	}
	defer em.Close()

	// create the docker client Config and HostConfig structures necessary to create and start the service
	config, hostconfig, err := configureContainer(a, client, conn, procFinished, service, serviceState)
	if err != nil {
		glog.Errorf("can't configure container: %v", err)
		return false, err
	}

	cjson, _ := json.MarshalIndent(config, "", "     ")
	glog.V(3).Infof(">>> CreateContainerOptions:\n%s", string(cjson))

	hcjson, _ := json.MarshalIndent(hostconfig, "", "     ")
	glog.V(3).Infof(">>> HostConfigOptions:\n%s", string(hcjson))

	// attempt to create the container, if it fails try to pull the image and then attempt to create it again
	ctr, err := dc.CreateContainer(docker.CreateContainerOptions{Name: serviceState.Id, Config: config})
	switch {
	case err == docker.ErrNoSuchImage:

		// get rid of the snapshot UUID from the ImageID before trying to pull it
		re := regexp.MustCompile("(?P<head>[[:alpha:]\\.]+\\/[[:alpha:]]+\\/)[[:alpha:][:digit:]-]+_(?P<tail>[[:alnum:]-]+)")
		if ok := re.MatchString(service.ImageId); !ok {
			glog.Errorf("can't determine repo from image id %s: %v", service.ImageId, err)
			return false, err
		}
		repo := fmt.Sprintf(re.ReplaceAllString(service.ImageId, fmt.Sprintf("${%s}${%s}", re.SubexpNames()[1], re.SubexpNames()[2])))

		glog.Infof("container pulling image %s Name:%s for service ID:%s Name:%s Cmd:%+v", repo, serviceState.Id, service.Id, service.Name, config.Cmd)

		pullopts := docker.PullImageOptions{
			Repository:   repo,
			OutputStream: os.NewFile(uintptr(syscall.Stdout), "/dev/stdout"),
		}
		pullerr := dc.PullImage(pullopts, docker.AuthConfiguration{})
		if pullerr != nil {
			glog.Errorf("can't pull container image %s: %v", service.ImageId, err)
			return false, err
		}

		ctr, err = dc.CreateContainer(docker.CreateContainerOptions{Name: serviceState.Id, Config: config})
		if err != nil {
			glog.Errorf("can't create container after pulling %v: %v", config, err)
			return false, err
		}
	case err != nil:
		// something that can't be fixed by pulling happened, we're done.
		glog.Errorf("can't create container %v: %v", config, err)
		return false, err
	}

	glog.Infof("container %s created  Name:%s for service Name:%s ID:%s Cmd:%+v", ctr.ID, serviceState.Id, service.Name, service.Id, config.Cmd)

	// use the docker client EventMonitor to listen for events from this container
	s, err := em.Subscribe(ctr.ID)
	if err != nil {
		glog.Errorf("can't subscribe to Docker events on container %s: %v", ctr.ID, err)
		return false, err
	}

	emc := make(chan struct{})

	s.Handle(docker.Start, func(e docker.Event) error {
		glog.Infof("container %s starting Name:%s for service Name:%s ID:%s Cmd:%+v", e["id"], serviceState.Id, service.Name, service.Id, config.Cmd)
		emc <- struct{}{}
		return nil
	})

	err = dc.StartContainer(ctr.ID, hostconfig)
	if err != nil {
		glog.Errorf("can't start container %s for service Name:%s ID:%s error: %v", ctr.ID, service.Name, service.Id, err)
		return false, err
	}

	// wait until we get notified that the container is started, or ten seconds, whichever comes first.
	// TODO: make the timeout configurable
	timeout := 10 * time.Second
	tout := time.After(timeout)
	select {
	case <-emc:
		glog.Infof("container %s started  Name:%s for service Name:%s ID:%s", ctr.ID, serviceState.Id, service.Name, service.Id)
	case <-tout:
		glog.Warningf("container %s start timed out after %v Name:%s for service Name:%s ID:%s Cmd:%+v", ctr.ID, timeout, serviceState.Id, service.Name, service.Id, config.Cmd)
		// FIXME: WORKAROUND for issue where docker.Start event doesn't always notify
		if container, err := dc.InspectContainer(ctr.ID); err != nil {
			glog.Warning("container %s could not be inspected error:%v\n\n", ctr.ID, err)
		} else {
			glog.Warningf("container %s inspected State:%+v", ctr.ID, container.State)
			if container.State.Running == true {
				glog.Infof("container %s start event timed out, but is running - will not return start timed out", ctr.ID)
				break
			}
		}
		return false, fmt.Errorf("start timed out")
	}

	glog.V(2).Infof("container %s a.waitForProcessToDie", ctr.ID)
	go a.waitForProcessToDie(dc, conn, ctr.ID, procFinished, serviceState)

	return true, nil
}

// configureContainer creates and populates two structures, a docker client Config and a docker client HostConfig structure
// that are used to create and start a container respectively. The information used to populate the structures is pulled from
// the service, serviceState, and conn values that are passed into configureContainer.
func configureContainer(a *HostAgent, client *ControlClient, conn coordclient.Connection, procFinished chan<- int, service *service.Service, serviceState *servicestate.ServiceState) (*docker.Config, *docker.HostConfig, error) {
	cfg := &docker.Config{}
	hcfg := &docker.HostConfig{}

	//get this service's tenantId for volume mapping
	var tenantID string
	err := client.GetTenantId(service.Id, &tenantID)
	if err != nil {
		glog.Errorf("Failed getting tenantID for service: %s, %s", service.Id, err)
	}

	// get the system user
	unused := 0
	systemUser := dao.User{}
	err = client.GetSystemUser(unused, &systemUser)
	if err != nil {
		glog.Errorf("Unable to get system user account for agent %s", err)
	}
	glog.V(1).Infof("System User %v", systemUser)

	cfg.Image = service.ImageId

	// get the endpoints
	cfg.ExposedPorts = make(map[docker.Port]struct{})
	hcfg.PortBindings = make(map[docker.Port][]docker.PortBinding)

	if service.Endpoints != nil {
		glog.V(1).Info("Endpoints for service: ", service.Endpoints)
		for _, endpoint := range service.Endpoints {
			if endpoint.Purpose == "export" { // only expose remote endpoints
				var p string
				switch endpoint.Protocol {
				case commons.UDP:
					p = fmt.Sprintf("%d/%s", endpoint.PortNumber, "udp")
				default:
					p = fmt.Sprintf("%d/%s", endpoint.PortNumber, "tcp")
				}
				cfg.ExposedPorts[docker.Port(p)] = struct{}{}
				hcfg.PortBindings[docker.Port(p)] = append(hcfg.PortBindings[docker.Port(p)], docker.PortBinding{})
			}
		}
	}

	if len(tenantID) == 0 && len(service.Volumes) > 0 {
		// FIXME: find a better way of handling this error condition
		glog.Fatalf("Could not get tenant ID and need to mount a volume, service state: %s, service id: %s", serviceState.Id, service.Id)
	}

	cfg.Volumes = make(map[string]struct{})
	hcfg.Binds = []string{}

	for _, volume := range service.Volumes {
		sv, err := getSubvolume(a.varPath, service.PoolId, tenantID, a.vfs)
		if err != nil {
			glog.Fatalf("Could not create subvolume: %s", err)
		} else {
			glog.Infof("Volume for service Name:%s ID:%s", service.Name, service.Id)

			resourcePath := path.Join(sv.Path(), volume.ResourcePath)
			glog.Infof("FullResourcePath: %s", resourcePath)
			if err = os.MkdirAll(resourcePath, 0770); err != nil {
				glog.Fatalf("Could not create resource path: %s, %s", resourcePath, err)
			}

			if err := createVolumeDir(resourcePath, volume.ContainerPath, service.ImageId, volume.Owner, volume.Permission); err != nil {
				glog.Errorf("Error populating resource path: %s with container path: %s, %v", resourcePath, volume.ContainerPath, err)
			}

			binding := fmt.Sprintf("%s:%s", resourcePath, volume.ContainerPath)
			cfg.Volumes[strings.Split(binding, ":")[1]] = struct{}{}
			hcfg.Binds = append(hcfg.Binds, strings.TrimSpace(binding))
		}
	}

	dir, binary, err := ExecPath()
	if err != nil {
		glog.Errorf("Error getting exec path: %v", err)
		return nil, nil, err
	}
	volumeBinding := fmt.Sprintf("%s:/serviced", dir)
	cfg.Volumes[strings.Split(volumeBinding, ":")[1]] = struct{}{}
	hcfg.Binds = append(hcfg.Binds, strings.TrimSpace(volumeBinding))

	if err := injectContext(service, client); err != nil {
		glog.Errorf("Error injecting context: %s", err)
		return nil, nil, err
	}

	// bind mount everything we need for logstash-forwarder
	if len(service.LogConfigs) != 0 {
		const LOGSTASH_CONTAINER_DIRECTORY = "/usr/local/serviced/resources/logstash"
		logstashPath := utils.ResourcesDir() + "/logstash"
		binding := fmt.Sprintf("%s:%s", logstashPath, LOGSTASH_CONTAINER_DIRECTORY)
		cfg.Volumes[LOGSTASH_CONTAINER_DIRECTORY] = struct{}{}
		hcfg.Binds = append(hcfg.Binds, binding)
		glog.V(1).Infof("added logstash bind mount: %s", binding)
	}

	// add arguments to mount requested directory (if requested)
	for _, bindMountString := range a.mount {
		splitMount := strings.Split(bindMountString, ",")
		numMountArgs := len(splitMount)

		if numMountArgs == 2 || numMountArgs == 3 {
			requestedImage := splitMount[0]
			hostPath := splitMount[1]

			// assume the container path is going to be the same as the host path
			containerPath := hostPath

			// if the container path is provided, use it
			if numMountArgs > 2 {
				containerPath = splitMount[2]
			}

			// insert tenantId into requestedImage - see dao.DeployService
			matchedRequestedImage := false
			if requestedImage == "*" {
				matchedRequestedImage = true
			} else {
				path := strings.SplitN(requestedImage, "/", 3)
				path[len(path)-1] = tenantID + "_" + path[len(path)-1]
				requestedImage = strings.Join(path, "/")
				matchedRequestedImage = (requestedImage == service.ImageId)
			}

			if matchedRequestedImage {
				binding := fmt.Sprintf("%s:%s", hostPath, containerPath)
				cfg.Volumes[strings.Split(binding, ":")[1]] = struct{}{}
				hcfg.Binds = append(hcfg.Binds, strings.TrimSpace(binding))
			}
		} else {
			glog.Warningf("Could not bind mount the following: %s", bindMountString)
		}
	}

	// add arguments for environment variables
	cfg.Env = append([]string{},
		"CONTROLPLANE=1",
		"CONTROLPLANE_CONSUMER_URL=http://localhost:22350/api/metrics/store",
		fmt.Sprintf("CONTROLPLANE_TENANT_ID=%s", tenantID),
		fmt.Sprintf("CONTROLPLANE_SYSTEM_USER=%s", systemUser.Name),
		fmt.Sprintf("CONTROLPLANE_SYSTEM_PASSWORD=%s", systemUser.Password))

	// add dns values to setup
	for _, addr := range a.dockerDNS {
		_addr := strings.TrimSpace(addr)
		if len(_addr) > 0 {
			cfg.Dns = append(cfg.Dns, addr)
		}
	}

	// Add hostname if set
	if service.Hostname != "" {
		cfg.Hostname = service.Hostname
	}

	cfg.Cmd = append([]string{},
		fmt.Sprintf("/serviced/%s", binary),
		"service",
		"proxy",
		service.Id,
		a.hostID,
		strconv.Itoa(serviceState.InstanceId),
		service.Startup)

	if service.Privileged {
		hcfg.Privileged = true
	}

	return cfg, hcfg, nil
}

// main loop of the HostAgent
func (a *HostAgent) start() {
	glog.V(1).Info("Starting HostAgent")
	for {
		// create a wrapping function so that client.Close() can be handled via defer
		keepGoing := func() bool {

			connc := make(chan coordclient.Connection)
			var conn coordclient.Connection
			done := make(chan struct{}, 1)
			defer func() {
				done <- struct{}{}
			}()
			go func() {
				for {
					c, err := a.zkClient.GetConnection()
					if err == nil {
						connc <- c
						return
					}
					// exit when our parent exits
					select {
					case <-done:
						return
					case <-time.After(time.Second):
					}
				}
				close(connc)
			}()
			select {
			case errc := <-a.closing:
				glog.V(0).Info("Received shutdown notice")
				a.zkClient.Close()
				errc <- errors.New("unable to connect to zookeeper")
				return false

			case conn = <-connc:
				glog.V(1).Info("Got a connected client")
			}
			defer conn.Close()
			return a.processChildrenAndWait(conn)
		}()
		if !keepGoing {
			break
		}
	}
}

type stateResult struct {
	id  string
	err error
}

// startMissingChildren accepts a zookeeper connection (conn) and a slice of service instance ids (children),
// a map of channels to signal running children stop, and a stateResult channel for children to signal when
// they shutdown
func (a *HostAgent) startMissingChildren(conn coordclient.Connection, children []string, processing map[string]chan int, ssDone chan stateResult) {
	glog.V(1).Infof("Agent for %s processing %d children", a.hostID, len(children))
	for _, childName := range children {
		if processing[childName] == nil {
			glog.V(2).Info("Agent starting goroutine to watch ", childName)
			childChannel := make(chan int, 1)
			processing[childName] = childChannel
			go a.processServiceState(conn, childChannel, ssDone, childName)
		}
	}
	return
}

func waitForSsNodes(processing map[string]chan int, ssResultChan chan stateResult) (err error) {
	for key, shutdown := range processing {
		glog.V(1).Infof("Agent signaling for %s to shutdown.", key)
		shutdown <- 1
	}

	// Wait for goroutines to shutdown
	for len(processing) > 0 {
		select {
		case ssResult := <-ssResultChan:
			glog.V(1).Infof("Goroutine finished %s", ssResult.id)
			if err == nil && ssResult.err != nil {
				err = ssResult.err
			}
			delete(processing, ssResult.id)
		}
	}
	glog.V(0).Info("All service state nodes are shut down")
	return
}

func (a *HostAgent) processChildrenAndWait(conn coordclient.Connection) bool {
	processing := make(map[string]chan int)
	ssDone := make(chan stateResult, 25)

	hostPath := zzk.HostPath(a.hostID)

	for {

		glog.V(3).Infof("creating hostdir: %s", hostPath)
		conn.CreateDir(hostPath)

		glog.V(3).Infof("getting children of %s", hostPath)
		children, zkEvent, err := conn.ChildrenW(hostPath)
		if err != nil {
			glog.V(0).Infof("Unable to read children, retrying: %s", err)
			select {
			case <-time.After(3 * time.Second):
				return true
			case errc := <-a.closing:
				glog.V(1).Info("Agent received interrupt")
				err = waitForSsNodes(processing, ssDone)
				errc <- err
				return false
			}
		}
		a.startMissingChildren(conn, children, processing, ssDone)

		select {

		case errc := <-a.closing:
			glog.V(1).Info("Agent received interrupt")
			err = waitForSsNodes(processing, ssDone)
			errc <- err
			return false

		case ssResult := <-ssDone:
			glog.V(1).Infof("Goroutine finished %s", ssResult.id)
			delete(processing, ssResult.id)

		case evt := <-zkEvent:
			glog.V(1).Info("Agent event: ", evt)
		}
	}
}

func (a *HostAgent) processServiceState(conn coordclient.Connection, shutdown <-chan int, done chan<- stateResult, ssID string) {
	procFinished := make(chan int, 1)
	var attached bool

	for {
		var hss zzk.HostServiceState
		zkEvent, err := zzk.LoadHostServiceStateW(conn, a.hostID, ssID, &hss)
		if err != nil {
			errS := fmt.Sprintf("Unable to load host service state %s: %v", ssID, err)
			glog.Error(errS)
			done <- stateResult{ssID, errors.New(errS)}
			return
		}
		if len(hss.ServiceStateId) == 0 || len(hss.ServiceId) == 0 {
			errS := fmt.Sprintf("Service for %s is invalid", zzk.HostServiceStatePath(a.hostID, ssID))
			glog.Error(errS)
			done <- stateResult{ssID, errors.New(errS)}
			return
		}

		var ss servicestate.ServiceState
		if err := zzk.LoadServiceState(conn, hss.ServiceId, hss.ServiceStateId, &ss); err != nil {
			errS := fmt.Sprintf("Host service state unable to load service state %s", ssID)
			glog.Error(errS)
			// This goroutine is watching a node for a service state that does not
			// exist or could not be loaded. We should *probably* delete this node.
			hssPath := zzk.HostServiceStatePath(a.hostID, ssID)
			if err := conn.Delete(hssPath); err != nil {
				glog.Warningf("Unable to delete host service state %s", hssPath)
			}
			done <- stateResult{ssID, errors.New(errS)}
			return
		}

		var svc service.Service
		if err := zzk.LoadService(conn, ss.ServiceId, &svc); err != nil {
			errS := fmt.Sprintf("Host service state unable to load service %s", ss.ServiceId)
			glog.Errorf(errS)
			done <- stateResult{ssID, errors.New(errS)}
			return
		}

		glog.V(1).Infof("Processing %s, desired state: %d", svc.Name, hss.DesiredState)

		switch {

		case hss.DesiredState == service.SVCStop:
			// This node is marked for death
			glog.V(1).Infof("Service %s was marked for death, quitting", svc.Name)
			if attached {
				err = a.terminateAttached(conn, procFinished, &ss)
			} else {
				err = a.terminateInstance(conn, &ss)
			}
			done <- stateResult{ssID, err}
			return

		case attached:
			// Something uninteresting happened. Why are we here?
			glog.V(1).Infof("Service %s is attached in a child goroutine", svc.Name)

		case hss.DesiredState == service.SVCRun &&
			ss.Started.Year() <= 1 || ss.Terminated.Year() > 2:
			// Should run, and either not started or process died
			glog.V(1).Infof("Service %s does not appear to be running; starting", svc.Name)
			attached, err = a.startService(conn, procFinished, &svc, &ss)

		case ss.Started.Year() > 1 && ss.Terminated.Year() <= 1:
			// Service superficially seems to be running. We need to attach
			glog.V(1).Infof("Service %s appears to be running; attaching", svc.Name)
			attached, err = a.attachToService(conn, procFinished, &ss, &hss)

		default:
			glog.V(0).Infof("Unhandled service %s", svc.Name)
		}

		if !attached || err != nil {
			errS := fmt.Sprintf("Service state %s unable to start or attach to process", ssID)
			glog.V(1).Info(errS)
			a.terminateInstance(conn, &ss)
			done <- stateResult{ssID, errors.New(errS)}
			return
		}

		glog.V(3).Infoln("Successfully processed state for %s", svc.Name)

		select {

		case <-shutdown:
			glog.V(0).Info("Agent goroutine will stop watching ", ssID)
			err = a.terminateAttached(conn, procFinished, &ss)
			if err != nil {
				glog.Errorf("Error terminating %s: %v", svc.Name, err)
			}
			done <- stateResult{ssID, err}
			return

		case <-procFinished:
			glog.V(1).Infof("Process finished %s", ssID)
			attached = false
			continue

		case evt := <-zkEvent:
			if evt.Type == coordclient.EventNodeDeleted {
				glog.V(0).Info("Host service state deleted: ", ssID)
				err = a.terminateAttached(conn, procFinished, &ss)
				if err != nil {
					glog.Errorf("Error terminating %s: %v", svc.Name, err)
				}
				done <- stateResult{ssID, err}
				return
			}

			glog.V(1).Infof("Host service state %s received event %v", ssID, evt)
			continue
		}
	}
}
