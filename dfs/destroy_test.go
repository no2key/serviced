// Copyright 2015 The Serviced Authors.
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

// +build unit

package dfs_test

import (
	"github.com/control-center/serviced/dfs/docker"
	"github.com/control-center/serviced/domain/registry"
	volumemocks "github.com/control-center/serviced/volume/mocks"
	. "gopkg.in/check.v1"
)

func (s *DFSTestSuite) TestDestroy_NoVolume(c *C) {
	vol := &volumemocks.Volume{}
	s.disk.On("Get", "Base").Return(&volumemocks.Volume{}, ErrTestVolumeNotFound)
	vol.On("Snapshots").Return([]string{}, nil)
	vol.On("Path").Return("/path/to/tenantID")
	s.index.On("SearchLibraryByTag", "Base", docker.Latest).Return([]registry.Image{}, nil)
	s.net.On("RemoveVolume", "/path/to/tenantID").Return(nil)
	s.net.On("Stop").Return(nil)
	s.net.On("Restart").Return(nil)
	s.disk.On("Remove", "Base").Return(nil)
	err := s.dfs.Destroy("Base")
	c.Assert(err, IsNil)
}

func (s *DFSTestSuite) TestDestroy_ErrSnapshots(c *C) {
	vol := &volumemocks.Volume{}
	s.disk.On("Get", "Base").Return(vol, nil)
	vol.On("Snapshots").Return([]string{}, ErrTestNoSnapshots)
	vol.On("Path").Return("/path/to/tenantID")
	s.index.On("SearchLibraryByTag", "Base", docker.Latest).Return([]registry.Image{}, nil)
	s.net.On("RemoveVolume", "/path/to/tenantID").Return(nil)
	s.net.On("Stop").Return(nil)
	s.net.On("Restart").Return(nil)
	s.disk.On("Remove", "Base").Return(nil)
	err := s.dfs.Destroy("Base")
	c.Assert(err, IsNil)
	vol = s.getVolumeFromSnapshot("Base2_Snapshot", "Base2")
	vol.On("Snapshots").Return([]string{"Base2_Snapshot"}, nil)
	vol.On("SnapshotInfo", "Base2_Snapshot").Return(nil, ErrTestSnapshotNotFound)
	vol.On("Path").Return("/path/to/tenantID")
	s.index.On("SearchLibraryByTag", "Base2", docker.Latest).Return([]registry.Image{}, nil)
	s.disk.On("Remove", "Base2").Return(nil)
	err = s.dfs.Destroy("Base2")
	c.Assert(err, IsNil)
}

func (s *DFSTestSuite) TestDestroy_NoRemove(c *C) {
	vol := &volumemocks.Volume{}
	s.disk.On("Get", "Base").Return(vol, nil)
	vol.On("Snapshots").Return([]string{}, nil)
	vol.On("Path").Return("/path/to/tenantID")
	s.index.On("SearchLibraryByTag", "Base", docker.Latest).Return([]registry.Image{}, nil)
	s.net.On("RemoveVolume", "/path/to/tenantID").Return(nil)
	s.net.On("Stop").Return(ErrTestServerRunning).Once()
	s.net.On("Restart").Return(nil)
	s.disk.On("Remove", "Base").Return(nil).Once()
	err := s.dfs.Destroy("Base")
	c.Assert(err, IsNil)
	s.net.On("Stop").Return(nil)
	s.net.On("Restart").Return(nil)
	s.disk.On("Remove", "Base").Return(ErrTestVolumeNotRemoved)
	err = s.dfs.Destroy("Base")
	c.Assert(err, Equals, ErrTestVolumeNotRemoved)
}

func (s *DFSTestSuite) TestDestroy_Success(c *C) {
	vol := &volumemocks.Volume{}
	s.disk.On("Get", "Base").Return(vol, nil)
	vol.On("Snapshots").Return([]string{}, nil)
	vol.On("Path").Return("/path/to/tenantID")
	s.index.On("SearchLibraryByTag", "Base", docker.Latest).Return([]registry.Image{}, nil)
	s.net.On("RemoveVolume", "/path/to/tenantID").Return(nil)
	s.net.On("Stop").Return(nil)
	s.net.On("Restart").Return(nil)
	s.disk.On("Remove", "Base").Return(nil)
	err := s.dfs.Destroy("Base")
	c.Assert(err, IsNil)
}
