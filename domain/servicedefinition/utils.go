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

package servicedefinition

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	log "github.com/Sirupsen/logrus"
)

var (
	ErrNoServiceJson = errors.New("directory does not contain a service.json file")
)

func getServiceDefinition(path string) (serviceDef *ServiceDefinition, err error) {

	// is path a dir
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("given path is not a directory")
	}

	// look for service.json
	serviceFile := fmt.Sprintf("%s/service.json", path)
	blob, err := ioutil.ReadFile(serviceFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoServiceJson
		} else {
			return nil, err
		}
	}

	// load blob
	svc := ServiceDefinition{}
	err = json.Unmarshal(blob, &svc)
	if err != nil {
		plog.WithFields(log.Fields{
			"path": path,
		}).Error("Could not unmarshal service")
		return nil, err
	}
	//Launch isn't usually in a file but it is required, this sets it to a default value if not set
	svc.NormalizeLaunch()
	svc.Name = filepath.Base(path)
	if svc.ConfigFiles == nil {
		svc.ConfigFiles = make(map[string]ConfigFile)
	}

	// look at sub services
	subServices := make(map[string]*ServiceDefinition)
	subpaths, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, subpath := range subpaths {
		switch {
		case subpath.Name() == "service.json":
			continue
		case subpath.Name() == "makefile": // ignoring makefiles present in service defs
			continue
		case subpath.Name() == "-CONFIGS-":
			if !subpath.IsDir() {
				return nil, fmt.Errorf("-CONFIGS- must be a director: %s", path)
			}
			getFiles := func(p string, f os.FileInfo, err error) error {
				if f.IsDir() {
					return nil
				}
				buffer, err := ioutil.ReadFile(p)
				if err != nil {
					return err
				}
				path, err := filepath.Rel(filepath.Join(path, subpath.Name()), p)
				if err != nil {
					return err
				}
				path = "/" + path
				if _, ok := svc.ConfigFiles[path]; !ok {
					svc.ConfigFiles[path] = ConfigFile{
						Filename: path,
						Content:  string(buffer),
					}
				} else {
					configFile := svc.ConfigFiles[path]
					configFile.Content = string(buffer)
					svc.ConfigFiles[path] = configFile
				}
				return nil
			}
			err = filepath.Walk(path+"/"+subpath.Name(), getFiles)
			if err != nil {
				return nil, err
			}
		case subpath.Name() == "FILTERS":
			if !subpath.IsDir() {
				return nil, fmt.Errorf(path + "/-FILTERS- must be a directory.")
			}
			filters, err := getFiltersFromDirectory(path + "/" + subpath.Name())
			if err != nil {
				plog.WithError(err).WithFields(log.Fields{
					"path": path,
					"subpath": subpath.Name(),
				}).Error("Unable to fetch filters")
			} else {
				svc.LogFilters = filters
			}
		case subpath.IsDir():
			subsvc, err := getServiceDefinition(path + "/" + subpath.Name())
			if err == nil {
				subServices[subpath.Name()] = subsvc
			} else if err != ErrNoServiceJson {
				return nil, err
			}
			// else just skip this subdirectory

		default:
			plog.WithFields(log.Fields{
				"path": path,
				"subpath": subpath.Name(),
			}).Debug("Unrecognized file")
		}
	}
	svc.Services = make([]ServiceDefinition, len(subServices))
	i := 0
	for _, subsvc := range subServices {
		svc.Services[i] = *subsvc
		i++
	}
	return &svc, err
}

// this function takes a filter directory and creates a map
// of filters by looking at the content in that directory.
// it is assumed the filter name is the name of the file minus
// the .conf part. So test.conf would be a filter named "test"
func getFiltersFromDirectory(path string) (filters map[string]string, err error) {
	filters = make(map[string]string)
	subpaths, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, subpath := range subpaths {
		filterName := subpath.Name()

		// make sure it is a valid filter
		if !strings.HasSuffix(filterName, ".conf") {
			plog.WithFields(log.Fields{
				"path": path,
				"filtername": filterName,
			}).Warning("Skipping filter because it doesn't have a .conf extension")
			continue
		}
		// read the contents and add it to our map
		filePath := path + "/" + filterName
		contents, err := ioutil.ReadFile(filePath)
		if err != nil {
			plog.WithFields(log.Fields{
				"filepath": filePath,
			}).Error("Unable to read filter file, skipping")
			continue
		}
		filterName = strings.TrimSuffix(filterName, ".conf")
		filters[filterName] = string(contents)
	}
	plog.WithFields(log.Fields{
		"path": path,
		"filters": filters,
	}).Debug("Found filters")
	return filters, nil
}
