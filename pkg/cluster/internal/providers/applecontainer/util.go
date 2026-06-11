/*
Copyright 2026 The Kubernetes Authors.

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
	"regexp"
	"runtime"
	"strings"

	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
)

// IsAvailable checks if the Apple container CLI is available in the system
func IsAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	cmd := exec.Command(binaryName, "--version")
	lines, err := exec.OutputLines(cmd)
	if err != nil || len(lines) != 1 {
		return false
	}
	return strings.HasPrefix(lines[0], "container CLI version")
}

// dnsDomainRegexp extracts the domain value from the [dns] section of
// `container system property ls` TOML output
var dnsDomainRegexp = regexp.MustCompile(`(?s)\[dns\][^[]*?domain\s*=\s*"([^"]+)"`)

// defaultDNSDomain returns the local DNS domain under which the runtime
// registers container hostnames, or "" if none is usable.
//
// Container hostnames resolve between containers only when both:
//   - a default domain is configured ([dns] domain in the runtime config)
//   - that domain is registered (`sudo container system dns create <domain>`)
//
// When available, node hostnames can be used instead of IPs, which keeps
// multi-node clusters working across container restarts (IPs are
// reassigned on restart, names are not).
func defaultDNSDomain() string {
	out, err := exec.Output(exec.Command(binaryName, "system", "property", "ls"))
	if err != nil {
		return ""
	}
	m := dnsDomainRegexp.FindSubmatch(out)
	if m == nil {
		return ""
	}
	domain := string(m[1])
	// verify the domain is registered with the embedded DNS service
	lines, err := exec.OutputLines(exec.Command(binaryName, "system", "dns", "ls"))
	if err != nil {
		return ""
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == domain {
			return domain
		}
	}
	return ""
}

// containerInspect models the JSON output of `container inspect` and
// `container ls --format json`
type containerInspect struct {
	Configuration containerConfiguration `json:"configuration"`
	Status        *containerStatus       `json:"status"`
}

type containerConfiguration struct {
	ID             string            `json:"id"`
	Labels         map[string]string `json:"labels"`
	PublishedPorts []publishedPort   `json:"publishedPorts"`
}

type publishedPort struct {
	ContainerPort int    `json:"containerPort"`
	HostAddress   string `json:"hostAddress"`
	HostPort      int    `json:"hostPort"`
	Proto         string `json:"proto"`
}

type containerStatus struct {
	State    string             `json:"state"`
	Networks []containerNetwork `json:"networks"`
}

type containerNetwork struct {
	Hostname    string `json:"hostname"`
	IPv4Address string `json:"ipv4Address"` // CIDR notation
	IPv6Address string `json:"ipv6Address"` // CIDR notation
	Network     string `json:"network"`
}

// listAllContainers returns all containers known to the runtime,
// including stopped ones
func listAllContainers() ([]containerInspect, error) {
	cmd := exec.Command(binaryName, "ls", "-a", "--format", "json")
	out, err := exec.Output(cmd)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list containers")
	}
	var containers []containerInspect
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, errors.Wrap(err, "failed to parse container list")
	}
	return containers, nil
}

// inspectContainer returns the details for a single container
func inspectContainer(name string) (*containerInspect, error) {
	cmd := exec.Command(binaryName, "inspect", name)
	out, err := exec.Output(cmd)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to inspect container %q", name)
	}
	var containers []containerInspect
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, errors.Wrap(err, "failed to parse container inspect output")
	}
	if len(containers) != 1 {
		return nil, errors.Errorf("expected one container from inspect, got %d", len(containers))
	}
	return &containers[0], nil
}
