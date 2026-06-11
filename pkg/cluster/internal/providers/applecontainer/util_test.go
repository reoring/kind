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
	"testing"
)

func Test_chooseDNSDomain(t *testing.T) {
	t.Parallel()

	// abbreviated outputs of `container system property ls`
	propertiesWithDomain := []byte(`[build]
cpus = 2

[container]
cpus = 4
memory = "1gb"

[dns]
domain = "kind"

[kernel]
binaryPath = "opt/kata/share/kata-containers/vmlinux-6.18.15-186"
`)
	propertiesEmptyDNS := []byte(`[container]
cpus = 4

[dns]

[kernel]
binaryPath = "opt/kata/share/kata-containers/vmlinux-6.18.15-186"
`)

	cases := []struct {
		name              string
		propertiesTOML    []byte
		registeredDomains []string
		want              string
	}{
		{
			name:              "domain configured and registered",
			propertiesTOML:    propertiesWithDomain,
			registeredDomains: []string{"DOMAIN", "kind"},
			want:              "kind",
		},
		{
			name:              "domain configured but not registered",
			propertiesTOML:    propertiesWithDomain,
			registeredDomains: []string{"DOMAIN"},
			want:              "",
		},
		{
			name:              "no dns section value",
			propertiesTOML:    propertiesEmptyDNS,
			registeredDomains: []string{"DOMAIN", "kind"},
			want:              "",
		},
		{
			name:              "no properties at all",
			propertiesTOML:    []byte(""),
			registeredDomains: []string{"DOMAIN", "kind"},
			want:              "",
		},
		{
			name:              "registered list with surrounding whitespace",
			propertiesTOML:    propertiesWithDomain,
			registeredDomains: []string{"DOMAIN", "  kind  "},
			want:              "kind",
		},
		{
			name: "domain key outside the dns section is ignored",
			propertiesTOML: []byte(`[registry]
domain = "docker.io"

[dns]

[kernel]
binaryPath = "vmlinux"
`),
			registeredDomains: []string{"DOMAIN", "docker.io"},
			want:              "",
		},
	}

	for _, tc := range cases {
		tc := tc // capture variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := chooseDNSDomain(tc.propertiesTOML, tc.registeredDomains)
			if got != tc.want {
				t.Errorf("chooseDNSDomain: expected %q, received %q", tc.want, got)
			}
		})
	}
}

// abbreviated but structurally real output of `container inspect`
// from container CLI 1.0.0
const inspectFixture = `[
  {
    "configuration" : {
      "id" : "kind-control-plane",
      "labels" : {
        "io.x-k8s.kind.cluster" : "kind",
        "io.x-k8s.kind.role" : "control-plane"
      },
      "publishedPorts" : [
        {
          "containerPort" : 6443,
          "count" : 1,
          "hostAddress" : "127.0.0.1",
          "hostPort" : 50571,
          "proto" : "tcp"
        }
      ],
      "resources" : {
        "cpus" : 4,
        "memoryInBytes" : 4294967296
      }
    },
    "id" : "kind-control-plane",
    "status" : {
      "networks" : [
        {
          "hostname" : "kind-control-plane",
          "ipv4Address" : "192.168.64.2\/24",
          "ipv4Gateway" : "192.168.64.1",
          "ipv6Address" : "fd81:7c72:5333:45c5:f45d:2bff:fe3e:9dc6\/64",
          "network" : "kind"
        }
      ],
      "state" : "running"
    }
  }
]`

func Test_containerInspectParsing(t *testing.T) {
	t.Parallel()

	var containers []containerInspect
	if err := json.Unmarshal([]byte(inspectFixture), &containers); err != nil {
		t.Fatalf("failed to parse inspect fixture: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, received %d", len(containers))
	}
	c := containers[0]

	if c.Configuration.ID != "kind-control-plane" {
		t.Errorf("wrong id: %q", c.Configuration.ID)
	}
	if cluster := c.Configuration.Labels[clusterLabelKey]; cluster != "kind" {
		t.Errorf("wrong cluster label: %q", cluster)
	}
	if role := c.Configuration.Labels[nodeRoleLabelKey]; role != "control-plane" {
		t.Errorf("wrong role label: %q", role)
	}
	if len(c.Configuration.PublishedPorts) != 1 {
		t.Fatalf("expected 1 published port, received %d", len(c.Configuration.PublishedPorts))
	}
	port := c.Configuration.PublishedPorts[0]
	if port.ContainerPort != 6443 || port.HostAddress != "127.0.0.1" || port.HostPort != 50571 || port.Proto != "tcp" {
		t.Errorf("wrong published port: %+v", port)
	}
	if c.Status == nil || len(c.Status.Networks) != 1 {
		t.Fatalf("expected 1 network attachment, received %+v", c.Status)
	}
	if got := stripCIDRSuffix(c.Status.Networks[0].IPv4Address); got != "192.168.64.2" {
		t.Errorf("wrong ipv4 address: %q", got)
	}
	if got := stripCIDRSuffix(c.Status.Networks[0].IPv6Address); got != "fd81:7c72:5333:45c5:f45d:2bff:fe3e:9dc6" {
		t.Errorf("wrong ipv6 address: %q", got)
	}
	if c.Status.State != "running" {
		t.Errorf("wrong state: %q", c.Status.State)
	}
}
