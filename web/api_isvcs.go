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
package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/control-center/serviced/dao"
	"github.com/control-center/serviced/domain"
	"github.com/control-center/serviced/domain/service"
	"github.com/control-center/serviced/health"
	"github.com/control-center/serviced/isvcs"

	"github.com/zenoss/go-json-rest"
)

func getAllInternalServices(w *rest.ResponseWriter, r *rest.Request, ctx *requestContext) {
	results := []interface{}{}

	for _, service := range getISVCS() {
		if service.ID == isvcs.InternalServicesISVC.ID {
			results = append(results, getParent())
		} else {
			results = append(results, getService(service))
		}
	}

	w.WriteJson(results)
}

func getInternalService(w *rest.ResponseWriter, r *rest.Request, ctx *requestContext) {
	id, err := url.QueryUnescape(r.PathParam("id"))
	if err != nil {
		writeJSON(w, err, http.StatusBadRequest)
		return
	} else if len(id) == 0 {
		writeJSON(w, "name must be specified", http.StatusBadRequest)
		return
	}

	if id == isvcs.InternalServicesISVC.ID {
		w.WriteJson(getParent())
		return
	}

	for _, service := range getISVCS() {
		if id == service.ID {
			w.WriteJson(getService(service))
			return
		}
	}

	writeJSON(w, "Internal Service Not Found.", http.StatusNotFound)
}

func getInternalServiceInstances(w *rest.ResponseWriter, r *rest.Request, ctx *requestContext) {
	id, err := url.QueryUnescape(r.PathParam("id"))
	if err != nil {
		writeJSON(w, err, http.StatusBadRequest)
		return
	} else if len(id) == 0 {
		writeJSON(w, "name must be specified", http.StatusBadRequest)
		return
	}

	instances := []interface{}{}
	for _, running := range getIRS() {
		if id == running.ID {
			instance := struct {
				InstanceID   int
				ServiceID    string
				ServiceName  string
				ContainerID  string
				DesiredState int
				HealthStatus map[string]health.Status
				Started      time.Time
			}{
				InstanceID:   running.InstanceID,
				ServiceID:    running.ServiceID,
				ServiceName:  running.Name,
				ContainerID:  running.DockerID,
				DesiredState: running.DesiredState,
				HealthStatus: getHealthStatus(running),
				Started:      running.StartedAt,
			}
			instances = append(instances, instance)
		}
	}

	if len(instances) > 0 {
		w.WriteJson(instances)
	} else {
		writeJSON(w, "Internal Service Not Found.", http.StatusNotFound)
	}
}

func getInternalServiceStatuses(w *rest.ResponseWriter, r *rest.Request, ctx *requestContext) {
	values := r.URL.Query()

	runningMap := make(map[string]dao.RunningService)
	for _, r := range getIRS() {
		runningMap[r.ID] = r
	}

	var ids []string
	if _, ok := values["id"]; ok {
		ids = values["id"]
	} else {
		for id := range runningMap {
			ids = append(ids, id)
		}
	}

	data := []interface{}{}

	for _, id := range ids {
		running := runningMap[id]

		instanceStatus := struct {
			InstanceID   int
			HealthStatus map[string]health.Status
		}{
			InstanceID:   running.InstanceID,
			HealthStatus: getHealthStatus(running),
		}

		serviceStatus := struct {
			ServiceID    string
			DesiredState int
			Status       []interface{}
		}{
			ServiceID:    running.ID,
			DesiredState: running.DesiredState,
			Status:       []interface{}{instanceStatus},
		}

		data = append(data, serviceStatus)
	}

	w.WriteJson(data)
}

func getParent() interface{} {
	return struct {
		ID                string
		Name              string
		Description       string
		Startup           string
		DeploymentID      string
		HasChildren       bool
		MonitoringProfile domain.MonitorProfile
	}{
		ID:                isvcs.InternalServicesISVC.ID,
		Name:              isvcs.InternalServicesISVC.Name,
		Description:       isvcs.InternalServicesISVC.Description,
		Startup:           isvcs.InternalServicesISVC.Startup,
		DeploymentID:      isvcs.InternalServicesISVC.DeploymentID,
		HasChildren:       true,
		MonitoringProfile: isvcs.InternalServicesISVC.MonitoringProfile,
	}
}

func getService(s service.Service) interface{} {
	return struct {
		ID                string
		Name              string
		Description       string
		Startup           string
		Parent            interface{}
		DeploymentID      string
		HasChildren       bool
		MonitoringProfile domain.MonitorProfile
	}{
		ID:                s.ID,
		Name:              s.Name,
		Description:       s.Description,
		Startup:           s.Startup,
		Parent:            getParent(),
		DeploymentID:      s.DeploymentID,
		HasChildren:       false,
		MonitoringProfile: s.MonitoringProfile,
	}
}

func getHealthStatus(running dao.RunningService) map[string]health.Status {
	var healthChecks map[string]health.HealthStatus

	healthStatusMap := make(map[string]health.Status)
	if running.ServiceID == "isvc-internalservices" {
		healthChecks = isvcsRootHealth
	} else {
		results, err := isvcs.Mgr.GetHealthStatus(strings.TrimPrefix(running.ServiceID, "isvc-"))
		if err != nil {
			healthStatusMap["alive"] = health.Unknown
			return healthStatusMap
		}
		healthChecks = convertDomainHealthToNewHealth(results.HealthStatuses)
	}

	for name, check := range healthChecks {
		healthStatusMap[name] = check.Status
	}
	return healthStatusMap
}
