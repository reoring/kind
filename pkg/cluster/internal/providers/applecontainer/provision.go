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
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/kind/pkg/cluster/constants"
	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
	"sigs.k8s.io/kind/pkg/fs"

	"sigs.k8s.io/kind/pkg/cluster/internal/loadbalancer"
	"sigs.k8s.io/kind/pkg/cluster/internal/providers/common"
	"sigs.k8s.io/kind/pkg/internal/apis/config"
)

// defaultNodeMemory is the memory allocated to each node VM.
// The Apple container runtime defaults to 1G which is not enough
// to run a kubernetes control plane.
const defaultNodeMemory = "4G"

// defaultNodeCPUs is the number of CPUs allocated to each node VM
const defaultNodeCPUs = "4"

// planCreation creates a slice of funcs that will create the containers
func planCreation(cfg *config.Cluster, networkName string) (createContainerFuncs []func() error, err error) {
	// these apply to all container creation
	nodeNamer := common.MakeNodeNamer(cfg.Name)
	names := make([]string, len(cfg.Nodes))
	for i, node := range cfg.Nodes {
		name := nodeNamer(string(node.Role)) // name the node
		names[i] = name
	}

	// the external load balancer resolves control-plane nodes by name
	// (envoy STRICT_DNS), which requires the runtime's local DNS domain
	haveLoadbalancer := config.ClusterHasImplicitLoadBalancer(cfg)
	if haveLoadbalancer {
		if defaultDNSDomain() == "" {
			return nil, errors.New("multiple control-plane nodes (HA) require the runtime's local DNS domain: " +
				"run `sudo container system dns create <domain>`, set it as [dns] domain in ~/.config/container/config.toml, " +
				"and restart the runtime with `container system stop && container system start`")
		}
		names = append(names, nodeNamer(constants.ExternalLoadBalancerNodeRoleValue))
	}

	genericArgs, err := commonArgs(cfg.Name, cfg, networkName, names)
	if err != nil {
		return nil, err
	}

	apiServerPort := cfg.Networking.APIServerPort
	apiServerAddress := cfg.Networking.APIServerAddress
	if haveLoadbalancer {
		// only the external LB reflects the API server port, the
		// control-plane nodes are published on random host ports
		apiServerPort = 0
		apiServerAddress = "127.0.0.1"
		if cfg.Networking.IPFamily == config.IPv6Family {
			apiServerAddress = "::1"
		}
		// plan loadbalancer node
		name := names[len(names)-1]
		createContainerFuncs = append(createContainerFuncs, func() error {
			args, err := runArgsForLoadBalancer(cfg, name, genericArgs)
			if err != nil {
				return err
			}
			return createContainer(name, args)
		})
	}

	// plan normal nodes
	for i, node := range cfg.Nodes {
		node := node.DeepCopy() // copy so we can modify
		name := names[i]

		// fixup relative paths, the runtime can only handle absolute paths
		for m := range node.ExtraMounts {
			hostPath := node.ExtraMounts[m].HostPath
			if !fs.IsAbs(hostPath) {
				absHostPath, err := filepath.Abs(hostPath)
				if err != nil {
					return nil, errors.Wrapf(err, "unable to resolve absolute path for hostPath: %q", hostPath)
				}
				node.ExtraMounts[m].HostPath = absHostPath
			}
		}

		// plan actual creation based on role
		switch node.Role {
		case config.ControlPlaneRole:
			createContainerFuncs = append(createContainerFuncs, func() error {
				node.ExtraPortMappings = append(node.ExtraPortMappings,
					config.PortMapping{
						ListenAddress: apiServerAddress,
						HostPort:      apiServerPort,
						ContainerPort: common.APIServerInternalPort,
					},
				)
				args, err := runArgsForNode(node, cfg.Networking.IPFamily, name, genericArgs)
				if err != nil {
					return err
				}
				return createNodeContainer(name, args)
			})
		case config.WorkerRole:
			createContainerFuncs = append(createContainerFuncs, func() error {
				args, err := runArgsForNode(node, cfg.Networking.IPFamily, name, genericArgs)
				if err != nil {
					return err
				}
				return createNodeContainer(name, args)
			})
		default:
			return nil, errors.Errorf("unknown node role: %q", node.Role)
		}
	}
	return createContainerFuncs, nil
}

// commonArgs computes static arguments that apply to all containers
func commonArgs(cluster string, cfg *config.Cluster, networkName string, nodeNames []string) ([]string, error) {
	// standard arguments all node containers need, computed once
	args := []string{
		"--detach", // run the container detached
		"--tty",    // allocate a tty for entrypoint logs
		// label the node with the cluster ID
		"--label", fmt.Sprintf("%s=%s", clusterLabelKey, cluster),
		// use a dedicated network for parity with the other providers
		"--network", networkName,
	}

	// pass proxy environment variables
	proxyEnv, err := getProxyEnv(cfg, networkName, nodeNames)
	if err != nil {
		return nil, errors.Wrap(err, "proxy setup error")
	}
	for key, val := range proxyEnv {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, val))
	}

	if cfg.Networking.DNSSearch != nil {
		args = append(args, "-e", "KIND_DNS_SEARCH="+strings.Join(*cfg.Networking.DNSSearch, " "))
	}

	return args, nil
}

func runArgsForNode(node *config.Node, clusterIPFamily config.ClusterIPFamily, name string, args []string) ([]string, error) {
	args = append([]string{
		// the entrypoint and kubernetes need to manage mounts, cgroups, etc.
		// each node is an isolated VM, so this is safe
		"--cap-add", "ALL",
		// runtime temporary storage
		"--tmpfs", "/tmp", // various things depend on working /tmp
		"--tmpfs", "/run", // systemd wants a writable /run
		// label the node with the role ID
		"--label", fmt.Sprintf("%s=%s", nodeRoleLabelKey, node.Role),
		// resource limits, the runtime default of 1G / 4 CPUs is too small
		"--memory", nodeMemory(),
		"--cpus", nodeCPUs(),
		// propagate KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER to the entrypoint script
		"-e", "KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER",
	},
		args...,
	)

	// convert mounts and port mappings to container run args
	args = append(args, generateMountBindings(node.ExtraMounts...)...)
	mappingArgs, err := generatePortMappings(clusterIPFamily, node.ExtraPortMappings...)
	if err != nil {
		return nil, err
	}
	args = append(args, mappingArgs...)

	switch node.Role {
	case config.ControlPlaneRole:
		args = append(args, "-e", "KUBECONFIG=/etc/kubernetes/admin.conf")
	}

	// finally, specify the image to run
	_, image := sanitizeImage(node.Image)
	return append(args, image), nil
}

func runArgsForLoadBalancer(cfg *config.Cluster, name string, args []string) ([]string, error) {
	args = append([]string{
		// label the node with the role ID
		"--label", fmt.Sprintf("%s=%s", nodeRoleLabelKey, constants.ExternalLoadBalancerNodeRoleValue),
	},
		args...,
	)

	// load balancer port mapping
	mappingArgs, err := generatePortMappings(cfg.Networking.IPFamily,
		config.PortMapping{
			ListenAddress: cfg.Networking.APIServerAddress,
			HostPort:      cfg.Networking.APIServerPort,
			ContainerPort: common.APIServerInternalPort,
		},
	)
	if err != nil {
		return nil, err
	}
	args = append(args, mappingArgs...)

	// finally, specify the image to run and its bootstrap command
	_, image := sanitizeImage(loadbalancer.Image)
	args = append(args, image)
	args = append(args, loadbalancer.GenerateBootstrapCommand(cfg.Name, name)...)

	return args, nil
}

// createContainer creates a container without waiting for it to boot,
// used for the external load balancer which does not run systemd
func createContainer(name string, args []string) error {
	return exec.Command(binaryName, append([]string{"run", "--name", name}, args...)...).Run()
}

// nodeMemory returns the memory to allocate to each node VM
func nodeMemory() string {
	if v := os.Getenv("KIND_EXPERIMENTAL_APPLE_CONTAINER_MEMORY"); v != "" {
		return v
	}
	return defaultNodeMemory
}

// nodeCPUs returns the number of CPUs to allocate to each node VM
func nodeCPUs() string {
	if v := os.Getenv("KIND_EXPERIMENTAL_APPLE_CONTAINER_CPUS"); v != "" {
		return v
	}
	return defaultNodeCPUs
}

func getProxyEnv(cfg *config.Cluster, networkName string, nodeNames []string) (map[string]string, error) {
	envs := common.GetProxyEnvs(cfg)
	// Specifically add the network subnets to NO_PROXY if we are using a proxy
	if len(envs) > 0 {
		subnets, err := getSubnets(networkName)
		if err != nil {
			return nil, err
		}

		noProxyList := append(subnets, envs[common.NOProxy])
		noProxyList = append(noProxyList, nodeNames...)
		// Add pod and service dns names to no_proxy to allow in cluster
		// Note: this is best effort based on the default CoreDNS spec
		// https://github.com/kubernetes/dns/blob/master/docs/specification.md
		// Any user created pod/service hostnames, namespaces, custom DNS services
		// are expected to be no-proxied by the user explicitly.
		noProxyList = append(noProxyList, ".svc", ".svc.cluster", ".svc.cluster.local")
		noProxyJoined := strings.Join(noProxyList, ",")
		envs[common.NOProxy] = noProxyJoined
		envs[strings.ToLower(common.NOProxy)] = noProxyJoined
	}
	return envs, nil
}

// generateMountBindings converts the mount list to a list of args
// '<HostPath>:<ContainerPath>[:options]', where 'options'
// is a comma-separated list of the following strings:
// 'ro', if the path is read only
func generateMountBindings(mounts ...config.Mount) []string {
	args := make([]string, 0, len(mounts))
	for _, m := range mounts {
		bind := fmt.Sprintf("%s:%s", m.HostPath, m.ContainerPath)
		if m.Readonly {
			bind += ":ro"
		}
		// NOTE: SELinux relabeling and mount propagation are not supported
		// by the apple container runtime, mounts are virtiofs shares
		args = append(args, "--volume", bind)
	}
	return args
}

// generatePortMappings converts the portMappings list to a list of args
func generatePortMappings(clusterIPFamily config.ClusterIPFamily, portMappings ...config.PortMapping) ([]string, error) {
	args := make([]string, 0, len(portMappings))
	for _, pm := range portMappings {
		// do provider internal defaulting
		// in a future API revision we will handle this at the API level and remove this
		if pm.ListenAddress == "" {
			switch clusterIPFamily {
			case config.IPv4Family, config.DualStackFamily:
				pm.ListenAddress = "0.0.0.0"
			case config.IPv6Family:
				pm.ListenAddress = "::"
			default:
				return nil, errors.Errorf("unknown cluster IP family: %v", clusterIPFamily)
			}
		}
		if string(pm.Protocol) == "" {
			pm.Protocol = config.PortMappingProtocolTCP // TCP is the default
		}

		// validate that the provider can handle this binding
		switch pm.Protocol {
		case config.PortMappingProtocolTCP:
		case config.PortMappingProtocolUDP:
		default:
			return nil, errors.Errorf("unsupported port mapping protocol for the apple container provider: %v", pm.Protocol)
		}

		// get a random port if necessary (port = 0)
		hostPort, releaseHostPortFn, err := common.PortOrGetFreePort(pm.HostPort, pm.ListenAddress)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get random host port for port mapping")
		}
		if releaseHostPortFn != nil {
			defer releaseHostPortFn()
		}

		// generate the actual mapping arg
		protocol := strings.ToLower(string(pm.Protocol))
		hostPortBinding := net.JoinHostPort(pm.ListenAddress, fmt.Sprintf("%d", hostPort))
		args = append(args, "--publish", fmt.Sprintf("%s:%d/%s", hostPortBinding, pm.ContainerPort, protocol))
	}
	return args, nil
}

// createNodeContainer creates a node container, waits for systemd to
// reach the multi-user target, then applies sysctls that the runtime
// cannot set at create time (it has no --sysctl flag)
func createNodeContainer(name string, args []string) error {
	if err := exec.Command(binaryName, append([]string{"run", "--name", name}, args...)...).Run(); err != nil {
		return errors.Wrapf(err, "failed to create node container %q", name)
	}

	logCtx, logCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer logCancel()
	logCmd := exec.CommandContext(logCtx, binaryName, "logs", "-f", name)
	if err := common.WaitUntilLogRegexpMatches(logCtx, logCmd, common.NodeReachedCgroupsReadyRegexp()); err != nil {
		return errors.Wrapf(err, "failed to wait for node %q to boot", name)
	}

	// kubeadm and the pod network require IP forwarding, which the runtime
	// does not enable by default and cannot set at create time
	if err := exec.Command(binaryName, "exec", name,
		"sysctl", "-w", "net.ipv4.ip_forward=1", "net.ipv6.conf.all.forwarding=1",
	).Run(); err != nil {
		return errors.Wrapf(err, "failed to enable IP forwarding on node %q", name)
	}
	return nil
}
