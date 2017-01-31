// Copyright 2016 The Serviced Authors.
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

package service

import (
	"path"
	"sync"
	"time"

	"github.com/control-center/serviced/coordinator/client"

	log "github.com/Sirupsen/logrus"
	"github.com/control-center/serviced/domain/service"
)

// HostStateHandler is the handler for running the HostListener
type HostStateHandler interface {

	// StopsContainer stops the container if the container exists and isn't
	// already stopped.
	StopContainer(serviceID string, instanceID int) error

	// AttachContainer attaches to an existing container for the service
	// instance. Returns nil channel if the container id doesn't match or if
	// the container has stopped. Channel reports the time that the container
	// has stopped.
	AttachContainer(state *ServiceState, serviceID string, instanceID int) (<-chan time.Time, error)

	// StartContainer creates and starts a new container for the given service
	// instance.  It returns relevant information about the container and a
	// channel that triggers when the container has stopped.
	StartContainer(cancel <-chan interface{}, serviceID string, instanceID int) (*ServiceState, <-chan time.Time, error)

	// RestartContainer asynchronously prepulls the latest image before
	// stopping the container.  It only returns an error if there is a problem
	// with docker and not if the container is not running or doesn't exist.
	RestartContainer(cancel <-chan interface{}, serviceID string, instanceID int) error

	// ResumeContainer resumes a paused container.  Returns nil if the
	// container has stopped or if it doesn't exist.
	ResumeContainer(serviceID string, instanceID int) error

	// PauseContainer pauses a running container.  Returns nil if the container
	// has stopped or if it doesn't exist.
	PauseContainer(serviceID string, instanceID int) error
}

// HostStateListener is the listener for monitoring service instances
type HostStateListener struct {
	hostid  string
	handler HostStateHandler
	conn    client.Connection

	shutdown chan interface{}
	active   *sync.WaitGroup
	passive  map[string]struct {
		s  *ServiceState
		ch <-chan time.Time
	}
	mu *sync.Mutex
}

// NewHostStateListener instantiates a new host state listener
func NewHostStateListener(hostid string, handler HostStateHandler) *HostStateListener {
	return &HostStateListener{
		hostid:   hostid,
		handler:  handler,
		shutdown: make(chan interface{}),
		active:   &sync.WaitGroup{},
		passive: make(map[string]struct {
			s  *ServiceState
			ch <-chan time.Time
		}),
		mu * sync.Mutex{},
	}
}

// Listen implements zzk.Listener.  It manages all spawned goroutines for the
// provided path.
func (l *HostStateListener) Listen(shutdown <-chan struct{}, conn client.Connection) {
	Listen(shutdown, conn, l)
}

// Shutdown implements zzk.Listener.  It cleans up orphaned nodes as it
// prepares the listener for shutdown.
func (l *HostStateListener) Shutdown() {
	close(l.shutdown)
	l.active.Wait()
	l.Post(map[string]struct{}{})
}

// SetConn implements zzk.Spawner.  It sets the zookeeper connection for the
// listener.
func (l *HostStateListener) SetConn(conn client.Connection) {
	l.conn = conn
}

// Path implements zzk.Spawner.  It returns the path to the parent
func (l *HostStateListener) Path() string {
	return path.Join("/hosts", l.hostid, "instances")
}

// Pre implements zzk.Spawner.  It is the synchronous action that is called
// before spawn gets called.
func (l *HostStateListener) Pre() {
	l.active.Add(1)
}

// Post synchronizes the passive thread list
func (l *HostStateListener) Post(p map[string]struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// do not exit until all goroutines have exited to prevent a race condition
	// where the instance is created after it has been designated to shut down
	wg := &sync.WaitGroup{}
	defer wg.Wait()

	for id, thread := range l.passive {
		if _, ok := p[id]; !ok {
			delete(l.passive, id)
			_, serviceid, instanceid, _ := ParseStateID(id)

			wg.Add(1)
			go func(req StateRequest, ch <-chan time.Time) {
				l.terminate(req, ch)
				wg.Done()
			}(StateRequest{HostID: l.hostid, ServiceID: serviceid, InstanceID: instanceid}, thread.ch)
		}
	}
}

// Spawn implements zzk.Spawner.  It starts a new watcher for the given child
// node
func (l *HostStateListener) Spawn(cancel <-chan struct{}, stateid string) {
	defer l.active.Done()

	logger := plog.WithField("stateid", stateid)

	// check valid state id
	_, serviceid, instanceid, err := ParseStateID(stateid)
	if err != nil {
		logger.WithError(err).Warn("Deleting invalid state id")
		if err := l.conn.Delete(path.Join(l.Path(), stateid)); err != nil && err != client.ErrNoNode {
			logger.WithError(err).Error("Could not delete invalid state id")
		}
		return
	}

	logger = logger.WithFields(log.Fields{
		"serviceid":  serviceid,
		"instanceid": instanceid,
	})

	// set up the request object for updates
	var (
		hspth = path.Join(l.Path(), stateid)               // host state path
		sspth = path.Join("/services", serviceid, stateid) // service state path
		req   = StateRequest{
			HostID:     l.hostid,
			ServiceID:  serviceid,
			InstanceID: instanceid,
		}
	)

	// get container information
	ssdat, exited := l.loadThread(req)
	if ssdat == nil {
		return
	}

	done := make(chan struct{})
	defer func() { close(done) }()
	for {
		// set up a listener on the host state node
		hsdat := &HostState{}
		hsevt, err := l.conn.GetW(hspth, hsdat, done)
		if err == client.ErrNoNode {
			logger.Debug("Host state was removed, exiting")
			l.terminate(req, exited)
			return
		} else if err != nil {
			logger.WithError(err).Warn("Could not watch host state, detaching from container")
			l.saveThread(stateid, ssdat, exited)
			return
		}

		// set up a listener on the service state node to ensure the node's
		// existance
		ok, ssevt, err := l.conn.ExistsW(sspth, done)
		if err != nil {
			logger.WithError(err).Error("Could not watch service state, detaching from container")
			l.saveThread(stateid, ssdat, exited)
			return
		} else if !ok {
			logger.Debug("Service state was removed, exiting")
			l.terminate(req, exited)
			return
		}

		switch hsdat.DesiredState {
		case service.SVCRun:
			if exited == nil {
				// container is not running, start it
				ssdat, exited, err = l.handler.StartContainer(l.shutdown, serviceid, instanceid)
				if err != nil {
					logger.WithError(err).Error("Could not start container, exiting")
					l.terminate(req, exited)
					return
				}
				if err := UpdateState(l.conn, req, func(s *State) bool {
					s.ServiceState = *ssdat
					return true
				}); err != nil {
					logger.WithError(err).Error("Could not update container state, detaching from container")
					l.saveThread(stateid, ssdat, exited)
					return
				}
				logger.Debug("Started container")
			} else if ssdat.Paused {
				// resume paused container
				if err := l.handler.ResumeContainer(serviceid, instanceid); err != nil {
					logger.WithError(err).Error("Could not resume paused container, exiting")
					l.terminate(req, exited)
					return
				}
				ssdat.Paused = false
				if err := UpdateState(l.conn, req, func(s *State) bool {
					s.ServiceState = *ssdat
					return true
				}); err != nil {
					logger.WithError(err).Error("Could not update container state, detaching from container")
					l.saveThread(stateid, ssdat, exited)
					return
				}
				logger.Debug("Resumed paused container")
			}
		case service.SVCRestart:
			if ssdat.Restarted.Before(ssdat.Started) {
				if err := l.handler.RestartContainer(l.shutdown, serviceid, instanceid); err != nil {
					logger.WithError(err).Error("Could not restart container, exiting")
					l.terminate(req, exited)
					return
				}
				ssdat.Restarted = time.Now()
				if err := UpdateState(l.conn, req, func(s *State) bool {
					s.ServiceState = *ssdat
					if s.DesiredState == service.SVCRestart {
						s.DesiredState = service.SVCRun
					}
					return true
				}); err != nil {
					logger.WithError(err).Error("Could not update container state, detaching from container")
					l.saveThread(stateid, ssdat, exited)
					return
				}
				logger.Debug("Initiating container restart")
			} else {
				if err := UpdateState(l.conn, req, func(s *State) bool {
					if s.DesiredState == service.SVCRestart {
						s.DesiredState = service.SVCRun
						return false
					}
					return true
				}); err != nil {
					logger.WithError(err).Error("Could not update desired state, detaching from container")
					l.saveThread(stateid, ssdat, exited)
					return
				}
				logger.Debug("Container restart already initiated")
			}
		case service.SVCPause:
			if exited != nil && !ssdat.Paused {
				// container is not paused, so pause the container
				if err := l.handler.PauseContainer(serviceid, instanceid); err != nil {
					logger.WithError(err).Error("Could not pause running container, exiting")
					l.terminate(req, exited)
					return
				}
				ssdat.Paused = true
				if err := UpdateState(l.conn, req, func(s *State) bool {
					s.ServiceState = *ssdat
					return true
				}); err != nil {
					logger.WithError(err).Error("Could not update container state, detaching from container")
					l.saveThread(stateid, ssdat, exited)
					return
				}
				logger.Debug("Paused running container")
			}
		case service.SVCStop:
			logger.Debug("Stopping service")
			l.terminate(req, exited)
			return
		default:
			logger.Warn("Unknown desired state")
		}

		select {
		case <-hsevt:
		case <-ssevt:
		case timeExit := <-exited:
			exited = nil
			ssdat.Terminated = timeExit
			if err := UpdateState(l.conn, req, func(s *State) bool {
				s.ServiceState = *ssdat
				return true
			}); err != nil {
				logger.WithError(err).Error("Could not update container state, detaching from container")
				l.saveThread(stateid, ssdat, exited)
				return
			}
		case <-cancel:
		}

		// cancel takes precedence
		select {
		case <-cancel:
			logger.Debug("Listener shut down, detaching from container")
			l.saveThread(stateid, ssdat, exited)
			return
		default:
		}

		close(done)
		done = make(chan struct{})
	}
}

// loadThread loads the thread from the passive map, otherwise returns the
// data from zookeeper.
func (l *HostStateListener) loadThread(req StateRequest) (s *ServiceState, ch <-chan time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var (
		id  = req.StateID()
		pth = path.Join("/services", req.ServiceID, id)
	)

	logger := plog.WithFields(log.Fields{
		"stateid":     id,
		"servicepath": pth,
	})

	// read the thread from the map
	thread, ok := l.passive[id]
	if !ok {
		// no orphaned thread found, so read from zookeeper
		s = &ServiceState{}
		if err := l.conn.Get(pth, s); err == client.ErrNoNode {
			// node does not exist, so clean up and exit
			if err := DeleteState(l.conn, req); err != nil {
				logger.WithError(err).Error("Could not clean up host state, exiting")
			}
			return nil, nil
		} else if err != nil {
			logger.WithError(err).Error("Could not look up service state, exiting")
			return nil, nil
		}

		// attach to container if service is running from a previous restart
		var err error
		ch, err = l.handler.AttachContainer(req.ServiceID, req.InstanceID, s)
		if err != nil {
			logger.WithError(err).Error("Could not attach to container, exiting")
			l.terminate(req, ch)
			return nil, nil
		}
	} else {
		// make sure the path still exists
		ok, err := l.conn.Exists(pth)
		if err != nil {
			logger.WithError(err).Error("Could not check existance of service state, exiting")
			return nil, nil
		}

		if ok {
			// set the state of the service in zookeeper
			if err := UpdateState(l.conn, req, func(s *State) bool {
				s.ServiceState = *thread.s
				return true
			}); err != nil {
				logger.WithError(err).Error("Could not update container state, exiting")
				return nil, nil
			}
			s, ch = thread.s, thread.ch
		} else {
			// path does not exist; clean up orphaned container.
			logger.Error("Service state not found, stopping orphaned container")
			l.terminate(req, thread.ch)
			s, ch = nil, nil
		}

		// switch this container from passive to active
		delete(l.passive, id)
	}
	return s, ch
}

// saveThread saves the thread to the passive map.
func (l *HostStateListener) saveThread(id string, s *ServiceState, ch <-chan time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	plog.WithField("stateid", id).Debug("Caching running instance")
	l.passive[id] = struct {
		s  *ServiceState
		ch <-chan time.Time
	}{s: s, ch: ch}
	return
}

// terminate shuts down running containers and cleans up applicable zookeeper
// data
func (l *HostStateListener) terminate(req StateRequest, ch <-chan time.Time) {
	logger := plog.WithFields(log.Fields{
		"serviceid":  req.ServiceID,
		"instanceid": req.InstanceID,
	})

	if err := l.handler.StopContainer(req.ServiceID, req.InstanceID); err != nil {
		logger.WithError(err).Error("Could not stop service instance")
	} else if ch != nil {
		logger.WithField("terminated", <-ch).Debug("Container has exited")
	}

	if err := DeleteState(l.conn, req); err != nil {
		logger.WithError(err).Error("Could not delete data associated with stopped instance")
	}
}
