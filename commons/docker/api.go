// Copyright 2014 The Serviced Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package docker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/control-center/serviced/commons"
	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/zenoss/glog"
)

const DockerLatest = "latest"

var DEFAULT_REGISTRY = "localhost:5000"
var pushLock sync.Mutex

// Container represents a Docker container.
type Container struct {
	*dockerclient.Container
	dockerclient.HostConfig
}

// ContainerDefinition records all the information necessary to create a Docker container.
type ContainerDefinition struct {
	dockerclient.CreateContainerOptions
	dockerclient.HostConfig
}

// ContainerActionFunc instances are used to handle container action notifications.
type ContainerActionFunc func(id string)

// Container subsystem error types
var (
	ErrAlreadyStarted  = errors.New("docker: container already started")
	ErrRequestTimeout  = errors.New("docker: request timed out")
	ErrKernelShutdown  = errors.New("docker: kernel shutdown")
	ErrNoSuchContainer = errors.New("docker: no such container")
	ErrNoSuchImage     = errors.New("docker: no such image")
)

// NewContainer creates a new container and returns its id. The supplied create action, if
// any, will be executed on successful creation of the container. If a start action is specified
// it will be executed after the container has been started. Note, if the start parameter is
// false the container won't be started and the start action will not be executed.
func NewContainer(cd *ContainerDefinition, start bool, timeout time.Duration, oncreate ContainerActionFunc, onstart ContainerActionFunc) (*Container, error) {

	args := struct {
		containerOptions *dockerclient.CreateContainerOptions
		hostConfig       *dockerclient.HostConfig
		start            bool
		createaction     ContainerActionFunc
		startaction      ContainerActionFunc
	}{&cd.CreateContainerOptions, &cd.HostConfig, start, oncreate, onstart}

	timeoutc := time.After(timeout)
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}

	em, err := dc.MonitorEvents()
	if err != nil {
		return nil, fmt.Errorf("can't monitor Docker events: %v", err)
	}

	iid, err := commons.ParseImageID(args.containerOptions.Config.Image)
	if err != nil {
		return nil, err
	}

	if _, err := FindImage(iid.String(), true); err != nil {
		glog.Errorf("Could not get image %s: %s", iid.String(), err)
		return nil, err
	}

	glog.V(2).Infof("creating container: %#v", *args.containerOptions)
	ctr, err := dc.CreateContainer(*args.containerOptions)
	switch {
	case err == dockerclient.ErrNoSuchImage:
		if err := PullImage(iid.String()); err != nil {
			glog.V(2).Infof("Unable to pull image %s: %v", iid.String(), err)
			return nil, err
		}
		ctr, err = dc.CreateContainer(*args.containerOptions)
		if err != nil {
			glog.V(2).Infof("container creation failed %+v: %v", *args.containerOptions, err)
			return nil, err
		}
	case err != nil:
		glog.V(2).Infof("container creation failed %+v: %v", *args.containerOptions, err)
		return nil, err
	}

	glog.V(2).Infof("created container: %+v", *ctr)
	if args.createaction != nil {
		args.createaction(ctr.ID)
	}

	if args.start {
		ss, err := em.Subscribe(ctr.ID)
		if err != nil {
			return nil, err
		}

		sc := make(chan struct{})

		ss.Handle(Start, func(e *dockerclient.APIEvents) error {
			if args.startaction != nil {
				args.startaction(ctr.ID)
			}
			glog.V(2).Infof("handling event: %+v for %s", e, ctr.ID)
			close(sc)
			return nil
		})
		defer ss.Cancel()

		glog.V(2).Infof("post creation start of %s: %+v", ctr.ID, args.hostConfig)
		err = dc.StartContainer(ctr.ID, args.hostConfig)
		if err != nil {
			glog.V(1).Infof("post creation start of %s failed: %v", ctr.ID, err)
			return nil, err
		}

		glog.V(2).Infof("======= wait for %s to start =======", ctr.ID)
		attempts := 0

	WaitForContainerStart:
		for {
			select {
			case <-timeoutc:
				glog.V(2).Infof("timeout starting container")
				return nil, fmt.Errorf("docker timeout starting container after %s", timeout)
			case <-sc:
				glog.V(2).Infof("update container %s state post start", ctr.ID)
				ctrID := ctr.ID
				ctr, err = dc.InspectContainer(ctrID)
				if err != nil {
					glog.V(1).Infof("failed to update container %s state post start: %v", ctrID, err)
					return nil, err
				}
				glog.V(2).Infof("container %s is started", ctr.ID)
				break WaitForContainerStart
			case <-time.After(5 * time.Second):
				nctr, err := dc.InspectContainer(ctr.ID)
				if err != nil {
					glog.V(2).Infof("can't inspect container %s: %v", ctr.ID, err)
					return nil, err
				}
				ctr = nctr

				switch {
				case !ctr.State.Running && attempts > maxStartAttempts:
					glog.V(2).Infof("timed out starting container")
					return nil, fmt.Errorf("timed out starting container: %s", ctr.ID)
				case !ctr.State.Running:
					attempts = attempts + 1
					continue WaitForContainerStart
				default:
					glog.V(2).Infof("container %s is running", ctr.ID)
					break WaitForContainerStart
				}
			}
		}
	}

	return &Container{ctr, cd.HostConfig}, nil
}

// FindContainer looks up a container using its id or name.
func FindContainer(id string) (*Container, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}

	ctr, err := dc.InspectContainer(id)
	if err != nil {
		if _, ok := err.(*dockerclient.NoSuchContainer); ok {
			return nil, ErrNoSuchContainer
		}
		return nil, err
	}
	return &Container{ctr, dockerclient.HostConfig{}}, nil
}

// Logs calls docker logs for a running service container
func Logs(dockerID string, args []string) error {
	if _, err := FindContainer(dockerID); err != nil {
		return err
	}

	var command []string = []string{"/usr/bin/docker", "logs"}
	if len(args) > 0 {
		command = append(command, args...)
	}
	command = append(command, dockerID)

	glog.V(1).Infof("exec logs command for container:%v command: %+v\n", dockerID, command)
	return syscall.Exec(command[0], command[0:], os.Environ())
}

// Containers retrieves a list of all the Docker containers.
func Containers() ([]*Container, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}
	apictrs, err := dc.ListContainers(dockerclient.ListContainersOptions{All: true})
	if err != nil {
		return nil, err
	}
	resp := []*Container{}
	for _, apictr := range apictrs {
		ctr, err := dc.InspectContainer(apictr.ID)
		if err != nil {
			continue
		}
		resp = append(resp, &Container{ctr, dockerclient.HostConfig{}})
	}
	return resp, nil
}

// CancelOnEvent cancels the action associated with the specified event.
func (c *Container) CancelOnEvent(event string) error {
	return cancelOnContainerEvent(event, c.ID)
}

// Commit creates a new Image from the containers changes.
func (c *Container) Commit(iidstr string, push bool) (*Image, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}
	iid, err := commons.ParseImageID(iidstr)
	if err != nil {
		return nil, err
	}

	img, err := dc.CommitContainer(
		dockerclient.CommitContainerOptions{
			Container:  c.ID,
			Repository: iid.BaseName(),
		})

	if err != nil {
		glog.V(1).Infof("unable to commit container %s: %v", c.ID, err)
		return nil, err
	}
	if push {
		err = pushImage(iid.BaseName(), iid.Registry(), iid.Tag)
	}
	return &Image{img.ID, *iid}, err
}

// Delete removes the container.
func (c *Container) Delete(volumes bool) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.RemoveContainer(dockerclient.RemoveContainerOptions{ID: c.ID, RemoveVolumes: volumes})
}

// Export writes the contents of the container's filesystem as a tar archive to outfile.
func (c *Container) Export(outfile *os.File) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.ExportContainer(dockerclient.ExportContainerOptions{c.ID, outfile})
}

// Kill sends a SIGKILL signal to the container. If the container is not started
// no action is taken.
func (c *Container) Kill() error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.KillContainer(dockerclient.KillContainerOptions{ID: c.ID, Signal: dockerclient.SIGKILL})
}

// Inspect returns information about the container specified by id.
func (c *Container) Inspect() (*dockerclient.Container, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}
	return dc.InspectContainer(c.ID)
}

// IsRunning inspects the container and returns true if it is running
func (c *Container) IsRunning() bool {
	cc, err := c.Inspect()
	if err != nil {
		return false
	}

	return cc.State.Running
}

// OnEvent adds an action for the specified event.
func (c *Container) OnEvent(event string, action ContainerActionFunc) error {
	return onContainerEvent(event, c.ID, action)
}

// Restart stops and then restarts a container.
func (c *Container) Restart(timeout time.Duration) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}

	return dc.RestartContainer(c.ID, uint(timeout.Seconds()))

}

// Start uses the information provided in the container definition cd to start a new Docker
// container. If a container can't be started before the timeout expires an error is returned.
func (c *Container) Start(timeout time.Duration) error {
	if c.State.Running != false {
		return nil
	}

	dc, err := getDockerClient()
	if err != nil {
		return err
	}

	args := struct {
		id         string
		hostConfig *dockerclient.HostConfig
	}{c.ID, &c.HostConfig}

	// check to see if the container is already running
	ctr, err := dc.InspectContainer(args.id)
	if err != nil {
		glog.V(1).Infof("unable to inspect container %s prior to starting it: %v", args.id, err)
		return err
	}

	if ctr.State.Running {
		return ErrAlreadyStarted
	}

	glog.V(2).Infof("starting container %s: %+v", args.id, args.hostConfig)
	err = dc.StartContainer(args.id, args.hostConfig)
	if err != nil {
		glog.V(2).Infof("unable to start %s: %v", args.id, err)
		return err
	}

	glog.V(2).Infof("update container %s state post start", args.id)
	ctr, err = dc.InspectContainer(args.id)
	if err != nil {
		glog.V(2).Infof("failed to update container %s state post start: %v", args.id, err)
		return err
	}
	c.Container = ctr

	return nil
}

// Stop stops the container specified by the id. If the container can't be stopped before the timeout
// expires an error is returned.
func (c *Container) Stop(timeout time.Duration) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.StopContainer(c.ID, uint(timeout.Seconds()))
}

// Wait blocks until the container stops or the timeout expires and then returns its exit code.
func (c *Container) Wait(timeout time.Duration) (int, error) {

	dc, err := getDockerClient()
	if err != nil {
		return -127, err
	}
	type waitResult struct {
		rc  int
		err error
	}
	errc := make(chan waitResult, 1)
	go func() {
		rc, err := dc.WaitContainer(c.ID)
		errc <- waitResult{rc, err}
	}()

	select {
	case <-time.After(timeout):
	case result := <-errc:
		return result.rc, result.err
	}
	return -127, ErrRequestTimeout
}

// Image represents a Docker image
type Image struct {
	UUID string
	ID   commons.ImageID
}

// Images returns a list of all the named images in the local repository
func Images() ([]*Image, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}
	opts := dockerclient.ListImagesOptions{All: false}
	imgs, err := dc.ListImages(opts)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile("<none>:<none>")

	resp := []*Image{}
	for _, img := range imgs {
		for _, repotag := range img.RepoTags {
			if len(re.FindString(repotag)) > 0 {
				continue
			}

			iid, err := commons.ParseImageID(repotag)
			if err != nil {
				return resp, err
			}
			resp = append(resp, &Image{img.ID, *iid})
		}
	}
	return resp, nil
}

// ImportImage creates a new image in the local repository from a file system archive.
func ImportImage(repotag, filename string) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	glog.V(1).Infof("importing image %s from %s", repotag, filename)
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	iid, err := commons.ParseImageID(repotag)
	if err != nil {
		return err
	}

	opts := dockerclient.ImportImageOptions{
		Repository:  iid.BaseName(),
		Source:      "-",
		InputStream: f,
		Tag:         iid.Tag,
	}

	if err = dc.ImportImage(opts); err != nil {
		glog.V(1).Infof("unable to import %s: %v", repotag, err)
		return err
	}
	return err
}

// FindImage looks up an image by repotag, e.g., zenoss/devimg, from the local repository
// TODO: add a FindImageByFilter that returns collections of images
func FindImage(repotag string, pull bool) (*Image, error) {
	glog.V(1).Infof("looking up image: %s (pull if neccessary %t)", repotag, pull)
	if pull {
		if err := PullImage(repotag); err != nil && err != ErrNoSuchImage {
			return nil, err
		}
	}
	return lookupImage(repotag)
}

// Delete remove the image from the local repository
func (img *Image) Delete() error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.RemoveImage(img.ID.String())
}

// Tag tags an image in the local repository
func (img *Image) Tag(tag string, push bool) (*Image, error) {

	iid, err := commons.ParseImageID(tag)
	if err != nil {
		return nil, err
	}

	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}

	args := struct {
		uuid     string
		name     string
		repo     string
		registry string
		tag      string
	}{img.UUID, img.ID.String(), iid.BaseName(), iid.Registry(), iid.Tag}

	glog.V(1).Infof("tagging image %s as: %s", args.repo, args.tag)
	opts := dockerclient.TagImageOptions{Repo: args.repo, Tag: args.tag, Force: true}
	err = dc.TagImage(args.name, opts)
	if err != nil {
		glog.V(1).Infof("unable to tag image %s: %v", args.repo, err)
		return nil, err
	}

	iid, err = commons.ParseImageID(fmt.Sprintf("%s:%s", args.repo, args.tag))
	if err != nil {
		return nil, err
	}

	if push {
		err = pushImage(args.repo, args.registry, args.tag)
	}

	return &Image{args.uuid, *iid}, err
}

func TagImage(img *Image, tag string, push bool) (*Image, error) {
	return img.Tag(tag, push)
}

func InspectImage(uuid string) (*dockerclient.Image, error) {
	dc, err := getDockerClient()
	if err != nil {
		return nil, err
	}
	return dc.InspectImage(uuid)
}

func (img *Image) Inspect() (*dockerclient.Image, error) {
	return InspectImage(img.UUID)
}

func SaveImages(outfile *os.File, repotags ...string) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}
	return dc.ExportImages(dockerclient.ExportImagesOptions{Names: repotags, OutputStream: outfile})
}

func (img *Image) Save(outfile *os.File) error {
	return SaveImages(outfile, img.ID.String())
}

func LoadImages(filename string) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}

	glog.V(1).Infof("importing images from %s", filename)
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	opts := dockerclient.LoadImageOptions{InputStream: f}
	return dc.LoadImage(opts)
}

func ImageHistory(uuid string) ([]*dockerclient.Image, error) {
	layers := make([]*dockerclient.Image, 0, 64)
	for uuid != "" {
		imageInfo, err := InspectImage(uuid)
		if err != nil {
			return layers, err
		}
		layers = append(layers, imageInfo)
		uuid = imageInfo.Parent
	}
	return layers, nil
}

func (img *Image) History() ([]*dockerclient.Image, error) {
	return ImageHistory(img.UUID)
}

func onContainerEvent(event, id string, action ContainerActionFunc) error {
	ec := make(chan error, 1)

	cmds.AddAction <- addactionreq{
		request{ec},
		struct {
			id     string
			event  string
			action ContainerActionFunc
		}{id, event, action},
	}

	select {
	case <-done:
		return ErrKernelShutdown
	case err, ok := <-ec:
		switch {
		case !ok:
			return nil
		default:
			return fmt.Errorf("docker: request failed: %v", err)
		}
	}
}

func cancelOnContainerEvent(event, id string) error {
	ec := make(chan error, 1)

	cmds.CancelAction <- cancelactionreq{
		request{ec},
		struct {
			id    string
			event string
		}{id, event},
	}

	select {
	case <-done:
		return ErrKernelShutdown
	case err, ok := <-ec:
		switch {
		case !ok:
			return nil
		default:
			return fmt.Errorf("docker: request failed: %v", err)
		}
	}
}

func lookupImage(repotag string) (*Image, error) {
	imgs, err := Images()
	if err != nil {
		return nil, err
	}

	iid, err := commons.ParseImageID(repotag)
	if err != nil {
		return nil, err
	}

	if len(iid.Tag) == 0 {
		repotag = repotag + ":latest"
	}

	for _, img := range imgs {
		if img.ID.String() == repotag {
			glog.V(1).Info("found: ", repotag)
			return img, nil
		}
	}

	return nil, ErrNoSuchImage
}

func PullImage(repotag string) error {
	// TODO: figure out a way to pass auth creds to the api
	cmd := exec.Command("docker", "pull", repotag)

	// Suppressing docker output (too chatty)
	if err := cmd.Run(); err != nil {
		glog.Errorf("Unable to pull image %s", repotag)
		return ErrNoSuchImage
	}

	return nil
}

func pullImage(repo, registry, tag string) error {

	dc, err := getDockerClient()
	if err != nil {
		return err
	}

	glog.V(0).Infof("Pulling image from repo: %s and registry: %s with tag: %s", repo, registry, tag)
	opts := dockerclient.PullImageOptions{
		Repository: repo,
		Registry:   registry,
		Tag:        tag,
	}
	defer func(stime time.Time) {
		duration := time.Now().Sub(stime)
		glog.V(0).Infof("Finished pulling image from repo: %s and registry: %s with tag: %s in %s", repo, registry, tag, duration)
	}(time.Now())

	// FIXME: Need to populate AuthConfiguration (eventually)
	err = dc.PullImage(opts, dockerclient.AuthConfiguration{})
	if err != nil {
		glog.V(2).Infof("failed to pull %s: %v", repo, err)
		return err
	}
	return nil
}

// PushImage pushes an image by repotag to local registry, e.g., zenoss/devimg, from the local docker repository
func PushImage(repotag string) error {
	iid, err := commons.ParseImageID(repotag)
	if err != nil {
		return err

	}
	return pushImage(iid.BaseName(), iid.Registry(), iid.Tag)
}

func pushImage(repo, registry, tag string) error {
	dc, err := getDockerClient()
	if err != nil {
		return err
	}

	if tag == "" {
		tag = DockerLatest
	}

	glog.V(0).Infof("Pushing image from repo: %s to registry: %s with tag: %s", repo, registry, tag)
	opts := dockerclient.PushImageOptions{
		Name:     repo,
		Registry: registry,
		Tag:      tag,
	}
	defer func(stime time.Time) {
		duration := time.Now().Sub(stime)
		glog.V(0).Infof("Finished pushing image from repo: %s to registry: %s with tag: %s in %s", repo, registry, tag, duration)
	}(time.Now())

	pushLock.Lock()
	defer pushLock.Unlock()
	// FIXME: Need to populate AuthConfiguration (eventually)
	err = dc.PushImage(opts, dockerclient.AuthConfiguration{})
	if err != nil {
		glog.V(2).Infof("Failed to push %s: %v", repo, err)
		return err
	}
	return nil
}
