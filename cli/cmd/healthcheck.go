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

package cmd

import (
	"fmt"
	"os"

	"github.com/codegangsta/cli"
)

// Initializer for serviced healthcheck subcommands
func (c *ServicedCli) initHealthCheck() {
	c.app.Commands = append(c.app.Commands, cli.Command{
		Name:        "healthcheck",
		Usage:       "Reports on health of serviced",
		Description: "serviced healthcheck [ISERVICENAME-1 [ISERVICENAME-2 ... [ISERVICENAME-N]]]",
		Before:      c.cmdHealthCheck,
	})
}

// serviced healthcheck list [ISERVICENAME]
func (c *ServicedCli) cmdHealthCheck(ctx *cli.Context) error {
	if ctx.Bool("help") {
		return nil
	}

	if results, err := c.driver.ServicedHealthCheck(ctx.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return c.exit(2)
	} else {

		// TODO - Something needs to be done here to show different healthchecks and different instances.
		//
		// However, we can not break backwards compatibility for the HA resource agents < 1.0.0.
		// For backwards compatibility with the HA resource agent, the output format does not matter.
		// What does matter is the process exit code.
		// A failed healtcheck for one or more of the *remote* isvcs should NOT trigger an exitStatus of 1.
		// The exitStatus should be 1 only if one of the isvcs running on the master node has a failed
		// healthcheck.
		exitStatus := 0
		t := NewTable("Service Name,Container Name,Container ID,Health Check,Status")
		t.Padding = 2
		for _, serviceHealth := range results {
			for _, status := range serviceHealth.HealthStatuses {
				if status.Status != "passed" {
					exitStatus = 1
				}
				if serviceHealth.ContainerID == "" {
					serviceHealth.ContainerID = "<none>"
				}

				t.AddRow(map[string]interface{}{
					"Service Name":   serviceHealth.ServiceName,
					"Container Name": serviceHealth.ContainerName,
					"Container ID":   serviceHealth.ContainerID[:min(12, len(serviceHealth.ContainerID))],
					"Health Check":   status.Name,
					"Status":         getCombinedStatus(status.Status, status.Failure),
				})
			}
		}
		t.Print()
		return c.exit(exitStatus)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getCombinedStatus(status, failure string) string {
	if failure == "" {
		return status
	}
	return fmt.Sprintf("%s - %s", status, failure)
}
