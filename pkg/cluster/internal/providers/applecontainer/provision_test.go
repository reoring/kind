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
	"reflect"
	"testing"

	"sigs.k8s.io/kind/pkg/internal/apis/config"
)

func Test_generateMountBindings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mounts []config.Mount
		want   []string
	}{
		{
			name:   "no mounts",
			mounts: nil,
			want:   []string{},
		},
		{
			name: "read-write mount",
			mounts: []config.Mount{
				{
					HostPath:      "/foo",
					ContainerPath: "/bar",
				},
			},
			want: []string{"--volume", "/foo:/bar"},
		},
		{
			name: "read-only mount",
			mounts: []config.Mount{
				{
					HostPath:      "/foo",
					ContainerPath: "/bar",
					Readonly:      true,
				},
			},
			want: []string{"--volume", "/foo:/bar:ro"},
		},
		{
			name: "multiple mounts",
			mounts: []config.Mount{
				{
					HostPath:      "/foo",
					ContainerPath: "/bar",
				},
				{
					HostPath:      "/baz",
					ContainerPath: "/quux",
					Readonly:      true,
				},
			},
			want: []string{"--volume", "/foo:/bar", "--volume", "/baz:/quux:ro"},
		},
	}

	for _, tc := range cases {
		tc := tc // capture variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := generateMountBindings(tc.mounts...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("generateMountBindings: expected %v, received %v", tc.want, got)
			}
		})
	}
}

func Test_generatePortMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		ipFamily     config.ClusterIPFamily
		portMappings []config.PortMapping
		want         []string
		wantErr      bool
	}{
		{
			name:     "fixed tcp port with listen address",
			ipFamily: config.IPv4Family,
			portMappings: []config.PortMapping{
				{
					ListenAddress: "127.0.0.1",
					HostPort:      6443,
					ContainerPort: 6443,
					Protocol:      config.PortMappingProtocolTCP,
				},
			},
			want: []string{"--publish", "127.0.0.1:6443:6443/tcp"},
		},
		{
			name:     "defaulted listen address and protocol",
			ipFamily: config.IPv4Family,
			portMappings: []config.PortMapping{
				{
					HostPort:      8080,
					ContainerPort: 80,
				},
			},
			want: []string{"--publish", "0.0.0.0:8080:80/tcp"},
		},
		{
			name:     "defaulted listen address for ipv6",
			ipFamily: config.IPv6Family,
			portMappings: []config.PortMapping{
				{
					HostPort:      8080,
					ContainerPort: 80,
					Protocol:      config.PortMappingProtocolUDP,
				},
			},
			want: []string{"--publish", "[::]:8080:80/udp"},
		},
		{
			name:     "sctp is not supported",
			ipFamily: config.IPv4Family,
			portMappings: []config.PortMapping{
				{
					HostPort:      8080,
					ContainerPort: 80,
					Protocol:      config.PortMappingProtocolSCTP,
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc // capture variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := generatePortMappings(tc.ipFamily, tc.portMappings...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, received %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("generatePortMappings: expected %v, received %v", tc.want, got)
			}
		})
	}
}
