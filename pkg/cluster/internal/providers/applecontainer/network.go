/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package applecontainer

import (
	"encoding/json"

	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
)

// fixedNetworkName is the name of the network that node containers are
// attached to, mirroring the convention of the other kind providers
const fixedNetworkName = "kind"

// networkInspect models the JSON output of `container network inspect`
type networkInspect struct {
	Status *struct {
		IPv4Subnet string `json:"ipv4Subnet"`
		IPv6Subnet string `json:"ipv6Subnet"`
	} `json:"status"`
}

// ensureNetwork checks if the named network exists, and creates it if not
func ensureNetwork(name string) error {
	if _, err := getSubnets(name); err == nil {
		// network already exists, we're good
		return nil
	}
	if err := exec.Command(binaryName, "network", "create", name).Run(); err != nil {
		return errors.Wrapf(err, "failed to create network %q", name)
	}
	return nil
}

// getSubnets returns the subnets for the named network
func getSubnets(name string) ([]string, error) {
	cmd := exec.Command(binaryName, "network", "inspect", name)
	out, err := exec.Output(cmd)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to inspect network %q", name)
	}
	var networks []networkInspect
	if err := json.Unmarshal(out, &networks); err != nil {
		return nil, errors.Wrap(err, "failed to parse network inspect output")
	}
	subnets := []string{}
	for _, network := range networks {
		if network.Status == nil {
			continue
		}
		if network.Status.IPv4Subnet != "" {
			subnets = append(subnets, network.Status.IPv4Subnet)
		}
		if network.Status.IPv6Subnet != "" {
			subnets = append(subnets, network.Status.IPv6Subnet)
		}
	}
	return subnets, nil
}
