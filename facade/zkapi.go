// Copyright 2014 The Serviced Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package facade

import (
	"fmt"
	"path"
	"time"

	log "github.com/Sirupsen/logrus"
	zkimgregistry "github.com/control-center/serviced/dfs/registry"
	"github.com/control-center/serviced/domain/applicationendpoint"
	"github.com/control-center/serviced/domain/host"
	"github.com/control-center/serviced/domain/pool"
	"github.com/control-center/serviced/domain/registry"
	"github.com/control-center/serviced/domain/service"
	"github.com/control-center/serviced/domain/servicestate"
	"github.com/control-center/serviced/zzk"
	zkd "github.com/control-center/serviced/zzk/docker"
	zkregistry "github.com/control-center/serviced/zzk/registry"
	zkr "github.com/control-center/serviced/zzk/registry2"
	zkservice "github.com/control-center/serviced/zzk/service"
	zks "github.com/control-center/serviced/zzk/service2"
	zkvirtualip "github.com/control-center/serviced/zzk/virtualips"
	"github.com/zenoss/glog"
)

func getZZK(f *Facade) ZZK {
	return &zkf{f}
}

type zkf struct {
	f *Facade
}

// UpdateService updates the service object and exposed public endpoints that
// are synced in zookeeper.
// TODO: we may want to combine these calls into a single transaction
func (zk *zkf) UpdateService(tenantID string, svc *service.Service, setLockOnCreate, setLockOnUpdate bool) error {
	logger := plog.WithFields(log.Fields{
		"tenantid":    tenantID,
		"serviceid":   svc.ID,
		"servicename": svc.Name,
		"poolid":      svc.PoolID,
	})

	// get the root-based connection to update the service's endpoints
	rootconn, err := zzk.GetLocalConnection("/")
	if err != nil {
		logger.WithError(err).Debug("Could not acquire a root-based connection to update the service's public endpoints in zookeeper")
		return err
	}

	// map all the public endpoints on the service
	pubmap := make(map[zkr.PublicPortKey]zkr.PublicPort)
	vhmap := make(map[zkr.VHostKey]zkr.VHost)

	for _, ep := range svc.Endpoints {
		// map the public ports
		for _, p := range ep.PortList {
			key := zkr.PublicPortKey{
				HostID:      "master",
				PortAddress: p.PortAddr,
			}
			pub := zkr.PublicPort{
				TenantID:    tenantID,
				Application: ep.Application,
				ServiceID:   svc.ID,
				Enabled:     p.Enabled,
				Protocol:    p.Protocol,
				UseTLS:      p.UseTLS,
			}
			pubmap[key] = pub
		}

		// map the vhosts
		for _, v := range ep.VHostList {
			key := zkr.VHostKey{
				HostID:    "master",
				Subdomain: v.Name,
			}
			vh := zkr.VHost{
				TenantID:    tenantID,
				Application: ep.Application,
				ServiceID:   svc.ID,
				Enabled:     v.Enabled,
			}
			vhmap[key] = vh
		}
	}

	// sync the registry
	if err := zkr.SyncServiceRegistry(rootconn, svc.ID, pubmap, vhmap); err != nil {
		logger.WithError(err).Debug("Could not update the service's public endpoints in zookeeper")
		return err
	}
	logger.Debug("Updated the service's public endpoints in zookeeper")

	// get the pool-based connection to update the service
	poolconn, err := zzk.GetLocalConnection(path.Join("/pools", svc.PoolID))
	if err != nil {
		logger.WithError(err).Debug("Could not acquire a pool-based connection to update the service in zookeeper")
		return err
	}

	if err := zks.UpdateService(poolconn, *svc, setLockOnCreate, setLockOnUpdate); err != nil {
		logger.WithError(err).Debug("Could not update the service in zookeeper")
		return err
	}
	logger.Debug("Updated the service in zookeeper")
	return nil
}

func (zk *zkf) RemoveService(svc *service.Service) error {
	// acquire the service lock to prevent that service from being scheduled
	// as it is being deleted
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(svc.PoolID))
	if err != nil {
		return err
	}
	// remove the global list of all vhosts deployed
	if rootconn, err := zzk.GetLocalConnection("/"); err != nil {
		return err
	} else if err := zkservice.RemoveServicePublicEndpoints(rootconn, svc); err != nil {
		return err
	}
	return zkservice.RemoveService(conn, svc.ID)
}

func (zk *zkf) WaitService(svc *service.Service, state service.DesiredState, cancel <-chan interface{}) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(svc.PoolID))
	if err != nil {
		return err
	}
	return zkservice.WaitService(cancel, conn, svc.ID, state)
}

func (zk *zkf) GetServiceStates(poolID string, states *[]servicestate.ServiceState, serviceIDs ...string) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(poolID))
	if err != nil {
		return err
	}
	*states, err = zkservice.GetServiceStates(conn, serviceIDs...)
	return err
}

func (zk *zkf) StopServiceInstance(poolID, hostID, stateID string) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	return zkservice.StopServiceInstance(conn, poolID, hostID, stateID)
}

func (z *zkf) CheckRunningPublicEndpoint(publicendpoint zkregistry.PublicEndpointKey, serviceID string) error {
	rootBasedConnection, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	per, err := zkregistry.PublicEndpointRegistry(rootBasedConnection)
	if err != nil {
		glog.Errorf("Error getting public endpoint registry: %v", err)
		return err
	}
	publicEndpointEphemeralNodes, err := per.GetPublicEndpointKeyChildren(rootBasedConnection, publicendpoint)
	if err != nil {
		glog.Errorf("GetPublicEndpointKeyChildren failed %v: %v", publicendpoint, err)
		return err
	}
	if len(publicEndpointEphemeralNodes) > 0 {
		if publicEndpoint := publicEndpointEphemeralNodes[0]; publicEndpoint.ServiceID != serviceID {
			err := fmt.Errorf("public end point %s is already running under service %s", publicendpoint, publicEndpoint.Application)
			return err
		}
	}
	return nil
}

func (z *zkf) AddHost(host *host.Host) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(host.PoolID))
	if err != nil {
		return err
	}
	return zks.AddHost(conn, *host)
}

func (z *zkf) UpdateHost(host *host.Host) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(host.PoolID))
	if err != nil {
		return err
	}
	return zks.UpdateHost(conn, *host)
}

func (z *zkf) RemoveHost(host *host.Host) error {
	// acquire the service lock to prevent services from being scheduled
	// to that pool
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(host.PoolID))
	if err != nil {
		return err
	}
	locker, err := zkservice.ServiceLock(conn)
	if err != nil {
		glog.Errorf("Could not initialize service lock: %s", err)
		return err
	}
	if err := locker.Lock(); err != nil {
		glog.Errorf("Could not disable service scheduling for pool %s: %s", host.PoolID, err)
		return err
	}
	defer locker.Unlock()
	cancel := make(chan struct{})
	go func() {
		defer close(cancel)
		<-time.After(2 * time.Minute)
	}()
	return zks.RemoveHost(cancel, conn, "", host.ID)
}

func (z *zkf) GetActiveHosts(poolID string, hosts *[]string) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	*hosts, err = zks.GetCurrentHosts(conn, poolID)
	return err
}

func (z *zkf) UpdateResourcePool(pool *pool.ResourcePool) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	return zks.UpdateResourcePool(conn, *pool)
}

func (z *zkf) RemoveResourcePool(poolID string) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	return zks.RemoveResourcePool(conn, poolID)
}

func (z *zkf) AddVirtualIP(virtualIP *pool.VirtualIP) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(virtualIP.PoolID))
	if err != nil {
		return err
	}
	return zkvirtualip.AddVirtualIP(conn, virtualIP)
}

func (z *zkf) RemoveVirtualIP(virtualIP *pool.VirtualIP) error {
	conn, err := zzk.GetLocalConnection(zzk.GeneratePoolPath(virtualIP.PoolID))
	if err != nil {
		return err
	}
	return zkvirtualip.RemoveVirtualIP(conn, virtualIP.IP)
}

func (z *zkf) GetRegistryImage(id string) (*registry.Image, error) {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return nil, err
	}
	return zkimgregistry.GetRegistryImage(conn, id)
}

func (z *zkf) SetRegistryImage(image *registry.Image) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}

	return zkimgregistry.SetRegistryImage(conn, *image)
}

func (z *zkf) DeleteRegistryImage(id string) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	return zkimgregistry.DeleteRegistryImage(conn, id)
}

func (z *zkf) DeleteRegistryLibrary(tenantID string) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	return zkimgregistry.DeleteRegistryImage(conn, tenantID)
}

func (z *zkf) LockServices(svcs []service.Service) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	nodes := make([]zks.ServiceLockNode, len(svcs))
	for i, svc := range svcs {
		nodes[i] = zks.ServiceLockNode{
			PoolID:    svc.PoolID,
			ServiceID: svc.ID,
		}
	}
	return zks.LockServices(conn, nodes)
}

func (z *zkf) UnlockServices(svcs []service.Service) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		return err
	}
	nodes := make([]zks.ServiceLockNode, len(svcs))
	for i, svc := range svcs {
		nodes[i] = zks.ServiceLockNode{
			PoolID:    svc.PoolID,
			ServiceID: svc.ID,
		}
	}
	return zks.UnlockServices(conn, nodes)
}

// Get a list of exported endpoints for the specified service from the Zookeeper namespace
func (zk *zkf) GetServiceEndpoints(tenantID, serviceID string, result *[]applicationendpoint.ApplicationEndpoint) error {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		glog.Errorf("Could not get connection to zookeeper: %s", err)
		return err
	}

	endpointRegisty, err := zkregistry.CreateEndpointRegistry(conn)
	if err != nil {
		glog.Errorf("Error getting endpoint registry: %s", err)
		return err
	}

	serviceEndpoints, err := endpointRegisty.GetServiceEndpoints(conn, tenantID, serviceID)
	if err != nil {
		glog.Errorf("Error getting endpoints: %s", err)
		return err
	}

	for _, endpoint := range serviceEndpoints {
		*result = append(*result, endpoint.ApplicationEndpoint)
	}
	return nil
}

// GetServiceStates2 returns all running instances for a service
// FIXME: update name when integration is complete
func (zk *zkf) GetServiceStates2(poolID, serviceID string) ([]zks.State, error) {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		glog.Errorf("Could not get connection to zookeeper: %s", err)
		return nil, err
	}

	return zks.GetServiceStates(conn, poolID, serviceID)
}

// GetHostStates returns all running instances for a host
func (zk *zkf) GetHostStates(poolID, hostID string) ([]zks.State, error) {
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		glog.Errorf("Could not get connection to zookeeper: %s", err)
		return nil, err
	}

	return zks.GetHostStates(conn, poolID, hostID)
}

// GetServiceState returns the state of a service from its service and instance
// id.
func (zk *zkf) GetServiceState(poolID, serviceID string, instanceID int) (*zks.State, error) {
	logger := plog.WithFields(log.Fields{
		"poolid":     poolID,
		"serviceid":  serviceID,
		"instanceid": instanceID,
	})

	// get the root-based connection to find the service instance
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		logger.WithError(err).Debug("Could not acquire root-based connection")
		return nil, err
	}

	// get the state host id
	hostID, err := zks.GetServiceStateHostID(conn, poolID, serviceID, instanceID)
	if err != nil {
		logger.WithError(err).Debug("Could not get host id for state")
		return nil, err
	}

	logger = logger.WithField("hostid", hostID)
	logger.Debug("Found service state on host")

	// set up the request
	req := zks.StateRequest{
		PoolID:     poolID,
		HostID:     hostID,
		ServiceID:  serviceID,
		InstanceID: instanceID,
	}

	state, err := zks.GetState(conn, req)
	if err != nil {
		logger.WithError(err).Debug("Could not get state information")
		return nil, err
	}

	logger.Debug("Loaded state information")
	return state, nil
}

// StopServiceInstance2 stops an instance of a service
// FIXME: get rid of the 2
func (zk *zkf) StopServiceInstance2(poolID, serviceID string, instanceID int) error {
	logger := plog.WithFields(log.Fields{
		"poolid":     poolID,
		"serviceid":  serviceID,
		"instanceid": instanceID,
	})

	// get the root-based connection to stop the service instance
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		logger.WithError(err).Debug("Could not acquire root-based connection")
		return err
	}

	// get the state host id
	hostID, err := zks.GetServiceStateHostID(conn, poolID, serviceID, instanceID)
	if err != nil {
		logger.WithError(err).Debug("Could not get host id for state")
		return err
	}

	logger = logger.WithField("hostid", hostID)

	// check if the host is online
	isOnline, err := zks.IsHostOnline(conn, poolID, hostID)
	if err != nil {
		logger.WithError(err).Debug("Could not check the online status of the host")
		return err
	}

	// set up the request
	req := zks.StateRequest{
		PoolID:     poolID,
		HostID:     hostID,
		ServiceID:  serviceID,
		InstanceID: instanceID,
	}

	// if the host is online, schedule the service to stop, otherwise delete the
	// service state.
	if isOnline {
		if err := zks.UpdateState(conn, req, func(s *zks.State) bool {
			if s.DesiredState != service.SVCStop {
				s.DesiredState = service.SVCStop
				return true
			}
			return false
		}); err != nil {
			logger.WithError(err).Debug("Could not schedule to stop service instance")
			return err
		}
		logger.Debug("Service instance scheduled to stop")
	} else {
		if err := zks.DeleteState(conn, req); err != nil {
			logger.WithError(err).Debug("Could not delete service instance from offline host")
			return err
		}
		logger.Debug("Service instance deleted from offline host")
	}
	return nil
}

// StopServiceInstances stops all instances for a service
func (zk *zkf) StopServiceInstances(poolID, serviceID string) error {
	logger := plog.WithFields(log.Fields{
		"poolid":    poolID,
		"serviceid": serviceID,
	})

	// get the root-based connection to stop the service instance
	conn, err := zzk.GetLocalConnection("/")
	if err != nil {
		logger.WithError(err).Debug("Could not acquire root-based connection")
		return err
	}

	// keep track of host online states
	onlineHosts := make(map[string]bool)

	// get all the state ids of the service
	reqs, err := zks.GetServiceStateIDs(conn, poolID, serviceID)
	if err != nil {
		logger.WithError(err).Debug("Could not get service state ids")
		return err
	}

	// for each state, if the host is online, stop the service;
	// if the host is offline delete the service.
	for _, req := range reqs {
		st8log := logger.WithField("instanceid", req.InstanceID)

		// check the host
		isOnline, ok := onlineHosts[req.HostID]
		if !ok {
			isOnline, err = zks.IsHostOnline(conn, poolID, req.HostID)
			if err != nil {
				logger.WithField("hostid", req.HostID).WithError(err).Warn("Could not check if host is online")
				continue
			}
			onlineHosts[req.HostID] = isOnline
		}

		// manage the service
		if isOnline {
			if err := zks.UpdateState(conn, req, func(s *zks.State) bool {
				if s.DesiredState != service.SVCStop {
					s.DesiredState = service.SVCStop
					return true
				}
				return false
			}); err != nil {
				st8log.WithError(err).Warn("Could not stop service instance")
				continue
			}
			st8log.Debug("Set service instance to stopped")
		} else {
			if err := zks.DeleteState(conn, req); err != nil {
				st8log.WithError(err).Warn("Could not delete service instance")
				continue
			}
			st8log.Debug("Deleted service instance")
		}
	}

	return nil
}

// SendDockerAction submits an action to the docker queue
func (zk *zkf) SendDockerAction(poolID, serviceID string, instanceID int, command string, args []string) error {
	logger := plog.WithFields(log.Fields{
		"poolid":     poolID,
		"serviceid":  serviceID,
		"instanceid": instanceID,
	})

	// get the pool-based connection to send the docker action
	conn, err := zzk.GetLocalConnection(path.Join("/pools", poolID))
	if err != nil {
		logger.WithError(err).Debug("Could not acquire pool-based connection")
		return err
	}

	// get the state host id
	hostID, err := zks.GetServiceStateHostID(conn, "", serviceID, instanceID)
	if err != nil {
		logger.WithError(err).Debug("Could not get host id for state")
		return err
	}

	logger = logger.WithField("hostid", hostID)

	// set up the action
	req := zkd.Action{
		HostID:   hostID,
		DockerID: fmt.Sprintf("%s-%d", serviceID, instanceID),
		Command:  append([]string{command}, args...),
	}

	// send the action
	if _, err := zkd.SendAction(conn, &req); err != nil {
		logger.WithError(err).Debug("Could not send docker action")
		return err
	}

	logger.Debug("Submitted docker action")
	return nil
}
