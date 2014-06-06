// Copyright 2014, The Serviced Authors. All rights reserved.
// Use of this source code is governed by a
// license that can be found in the LICENSE file.

package registry

import (
	"github.com/zenoss/glog"
	"github.com/zenoss/serviced/coordinator/client"
)

type registryType struct {
	getPath func(nodes ...string) string
}

type WatchError func(path string, err error)

type processChildrenFunc func(conn client.Connection, parentPath string, nodeIDs ...string)

//Add key to the registry.  Returns the path of the key in the registry
func (r *registryType) AddKey(conn client.Connection, key string) (string, error) {
	path := r.getPath(key)
	if err := conn.CreateDir(path); err != nil {
		return "", err
	}
	return path, nil
}

func (r *registryType) WatchKey(conn client.Connection, key string, processChildren processChildrenFunc, errorHandler WatchError) error {
	keyPath := r.getPath(key)
	return watch(conn, keyPath, processChildren, errorHandler)
}

func (r *registryType) WatchRegistry(conn client.Connection, processChildren processChildrenFunc, errorHandler WatchError) error {
	path := r.getPath()
	return watch(conn, path, processChildren, errorHandler)
}

//Add VhostEndpoint to the key in registry.  Returns the path of the node in the registry
func (r *registryType) addItem(conn client.Connection, key string, nodeID string, node client.Node) (string, error) {
	if err := r.ensureDir(conn, r.getPath(key)); err != nil {
		glog.Errorf(" error key path:%s", r.getPath(key))
		return "", err
	}

	//TODO: make ephemeral
	path := r.getPath(key, nodeID)
	glog.Infof("Adding to %s: %#v", path, node)
	if err := conn.Create(path, node); err != nil {
		return "", err
	}
	return path, nil
}

func (r *registryType) ensureDir(conn client.Connection, path string) error {
	if exists, err := conn.Exists(path); err != nil {
		return err
	} else if !exists {
		if err := conn.CreateDir(path); err != nil {
			return err
		}
	}
	return nil
}

func watch(conn client.Connection, path string, processChildren processChildrenFunc, errorHandler WatchError) error {
	for {
		nodeIDs, event, err := conn.ChildrenW(path)
		if err != nil {
			glog.Errorf("Could not watch %s: %s", path, err)
			defer errorHandler(path, err)
			return err
		}
		processChildren(conn, path, nodeIDs...)
		//This blocks until a change happens under the key
		<-event
	}
	return nil
}

func (r *registryType) watchItem(conn client.Connection, path string, nodeType client.Node, processNode func(conn client.Connection,
	node client.Node), errorHandler WatchError) error {
	for {
		event, err := conn.GetW(path, nodeType)
		if err != nil {
			glog.Errorf("Could not watch %s: %s", path, err)
			defer errorHandler(path, err)
			return err
		}
		processNode(conn, nodeType)
		//This blocks until a change happens under the key
		<-event
	}
	return nil
}
