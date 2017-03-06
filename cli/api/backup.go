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

package api

import (
	"fmt"
	"path/filepath"

	"github.com/control-center/serviced/config"
	"github.com/control-center/serviced/dao"
	"github.com/control-center/serviced/volume"
	"github.com/dustin/go-humanize"
	"errors"
)


type BackupDetails struct {
	Available uint64
	EstimatedBytes uint64
	Estimated dao.BackupActual
	Path string
	Excludes []string
	EstCompr float64
	MinOverhead string
	Warn bool
	Deny bool
	Message string
}

// Dump all templates and services to a tgz file.
// This includes a snapshot of all shared file systems
// and exports all docker images the services depend on.
func (a *api) Backup(dirpath string, excludes []string) (string, error) {
	client, err := a.connectDAO()
	if err != nil {
		return "", err
	}
	// TODO: (?) add check for space here (or just handle error from client.Backup call?)
	var path string
	req := dao.BackupRequest{
		Dirpath:              dirpath,
		SnapshotSpacePercent: config.GetOptions().SnapshotSpacePercent,
		Excludes:             excludes,
	}
	if err := client.Backup(req, &path); err != nil {
		return "", err
	}

	return path, nil
}

// Restores templates, services, snapshots, and docker images from a tgz file.
// This is the inverse of CmdBackup.
func (a *api) Restore(path string) error {
	client, err := a.connectDAO()
	if err != nil {
		return err
	}

	fp, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("could not convert '%s' to an absolute file path: %v", path, err)
	}

	return client.Restore(filepath.Clean(fp), &unusedInt)
}


func (a *api) GetBackupSpace(dirpath string, excludes []string) (*BackupDetails, error) {
	fmt.Printf("Hello, from GetBackupSpace()\n")
	client, err := a.connectDAO()
	fmt.Printf("Back from connectDAO(). err = %v\n", err)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Error in connectDAO(): %v", err))
	}
	req := dao.BackupRequest{
		Dirpath:              dirpath,
		SnapshotSpacePercent: config.GetOptions().SnapshotSpacePercent,
		Excludes:             excludes,
	}
	fmt.Printf("Made BackupRequest: %v\n", req)
	ec := config.GetOptions().BackupEstimatedCompression
	fmt.Printf("got ec: %f\n", ec)
	mo := config.GetOptions().BackupMinOverhead
	fmt.Printf("got mo: %s\n", mo)
	minOverheadBytes, err := humanize.ParseBytes(mo)
	if err != nil {
		fmt.Printf("Error humanizing mo: %v\n", err)
		return nil, errors.New(fmt.Sprintf("error calling ParseBytes(%s): %v", mo, err))
		//return nil, err
	}
	est := dao.BackupActual{}
	if err := client.GetBackupEstimate(req, &est); err != nil {
		return nil, errors.New(fmt.Sprintf("error calling GetBackupestimate(): %v", err))
		//return nil, err
	}
	avail := volume.FilesystemBytesAvailable(dirpath)
	estBytes := uint64(float64(est.TotalBytesRequired) / ec)
	warn := (avail - estBytes) < minOverheadBytes
	deny := avail < estBytes
	message := ""
	if deny {
		message = fmt.Sprintf("Cannot take backup. Available space on %s is %s, and backup is estimated to take %s", dirpath, humanize.Bytes(avail), humanize.Bytes(estBytes))
	} else if warn {
		message = fmt.Sprintf("Backup not recommended. Available space on %s is %s, and backup is estimated to take %s, which would leave less than %s.", dirpath, humanize.Bytes(avail), humanize.Bytes(estBytes), humanize.Bytes(minOverheadBytes))
	} else {
		message = fmt.Sprintf("There should be sufficient room for a backup. Free space on %s is %s, and the backup is estimated to take %s, which will leave %s", dirpath, humanize.Bytes(avail), humanize.Bytes(estBytes), humanize.Bytes(avail - estBytes))
	}
	deets := BackupDetails{
		Available: avail,
		EstimatedBytes: estBytes,
		Estimated: est,
		Path: dirpath,
		Excludes: excludes,
		EstCompr: ec,
		MinOverhead: mo,
		Warn: warn,
		Deny: deny,
		Message: message,
	}

	return &deets, nil
}