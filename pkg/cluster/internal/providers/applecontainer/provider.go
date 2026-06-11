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
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/errors"
	"sigs.k8s.io/kind/pkg/exec"
	"sigs.k8s.io/kind/pkg/log"

	"sigs.k8s.io/kind/pkg/cluster/internal/providers"
	"sigs.k8s.io/kind/pkg/cluster/internal/providers/common"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
	"sigs.k8s.io/kind/pkg/internal/apis/config"
	"sigs.k8s.io/kind/pkg/internal/cli"
	"sigs.k8s.io/kind/pkg/internal/sets"
)

// NewProvider returns a new provider based on executing the Apple
// `container` CLI, see: https://github.com/apple/container
func NewProvider(logger log.Logger) providers.Provider {
	return &provider{
		logger: logger,
	}
}

// Provider implements provider.Provider
// see NewProvider
type provider struct {
	logger log.Logger
}

// String implements fmt.Stringer
// NOTE: the value of this should not currently be relied upon for anything!
// This is only used for setting the Node's providerID
func (p *provider) String() string {
	return "applecontainer"
}

// Provision is part of the providers.Provider interface
func (p *provider) Provision(status *cli.Status, cfg *config.Cluster) (err error) {
	// ensure node images are pulled before actually provisioning
	if err := ensureNodeImages(p.logger, status, cfg); err != nil {
		return err
	}

	// ensure the pre-requisite network exists
	if err := ensureNetwork(fixedNetworkName); err != nil {
		return errors.Wrap(err, "failed to ensure the kind network")
	}

	// actually provision the cluster
	icons := strings.Repeat("📦 ", len(cfg.Nodes))
	status.Start(fmt.Sprintf("Preparing nodes %s", icons))
	defer func() { status.End(err == nil) }()

	// plan creating the containers
	createContainerFuncs, err := planCreation(cfg, fixedNetworkName)
	if err != nil {
		return err
	}

	// actually create nodes
	// NOTE: sequential creation since each node boots a VM, this keeps
	// resource usage and log output predictable
	for _, f := range createContainerFuncs {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

// ListClusters is part of the providers.Provider interface
func (p *provider) ListClusters() ([]string, error) {
	containers, err := listAllContainers()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list clusters")
	}
	clusters := sets.NewString()
	for _, c := range containers {
		if cluster, ok := c.Configuration.Labels[clusterLabelKey]; ok {
			clusters.Insert(cluster)
		}
	}
	return clusters.List(), nil
}

// ListNodes is part of the providers.Provider interface
func (p *provider) ListNodes(cluster string) ([]nodes.Node, error) {
	containers, err := listAllContainers()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list nodes")
	}
	ret := []nodes.Node{}
	for _, c := range containers {
		if c.Configuration.Labels[clusterLabelKey] == cluster {
			ret = append(ret, p.node(c.Configuration.ID))
		}
	}
	return ret, nil
}

// DeleteNodes is part of the providers.Provider interface
func (p *provider) DeleteNodes(n []nodes.Node) error {
	if len(n) == 0 {
		return nil
	}
	names := make([]string, 0, len(n))
	for _, node := range n {
		names = append(names, node.String())
	}
	// attempt a graceful stop first, then force delete
	// (a force delete alone also kills the VM, but stopping first
	// gives systemd a chance to shut down cleanly)
	_ = exec.Command(binaryName, append([]string{"stop"}, names...)...).Run()
	if err := exec.Command(binaryName, append([]string{"rm", "-f"}, names...)...).Run(); err != nil {
		return errors.Wrap(err, "failed to delete nodes")
	}
	return nil
}

// GetAPIServerEndpoint is part of the providers.Provider interface
func (p *provider) GetAPIServerEndpoint(cluster string) (string, error) {
	// locate the node that hosts this
	allNodes, err := p.ListNodes(cluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to list nodes")
	}
	n, err := nodeutils.APIServerEndpointNode(allNodes)
	if err != nil {
		return "", errors.Wrap(err, "failed to get api server endpoint")
	}

	container, err := inspectContainer(n.String())
	if err != nil {
		return "", errors.Wrap(err, "failed to get api server port")
	}
	for _, pp := range container.Configuration.PublishedPorts {
		if pp.ContainerPort == common.APIServerInternalPort && strings.EqualFold(pp.Proto, "tcp") {
			return net.JoinHostPort(pp.HostAddress, fmt.Sprintf("%d", pp.HostPort)), nil
		}
	}
	return "", errors.Errorf("unable to find api server port mapping for node %q", n.String())
}

// GetAPIServerInternalEndpoint is part of the providers.Provider interface
func (p *provider) GetAPIServerInternalEndpoint(cluster string) (string, error) {
	// locate the node that hosts this
	allNodes, err := p.ListNodes(cluster)
	if err != nil {
		return "", errors.Wrap(err, "failed to list nodes")
	}
	n, err := nodeutils.APIServerEndpointNode(allNodes)
	if err != nil {
		return "", errors.Wrap(err, "failed to get api server endpoint")
	}
	// NOTE: the apple container runtime does not provide name resolution
	// between containers, so we use the node IP rather than its hostname
	ipv4, ipv6, err := n.IP()
	if err != nil {
		return "", errors.Wrap(err, "failed to get api server IP")
	}
	ip := ipv4
	if ip == "" {
		ip = ipv6
	}
	return net.JoinHostPort(ip, fmt.Sprintf("%d", common.APIServerInternalPort)), nil
}

// node returns a new node handle for this provider
func (p *provider) node(name string) nodes.Node {
	return &node{
		name: name,
	}
}

// CollectLogs will populate dir with cluster logs and other debug files
func (p *provider) CollectLogs(dir string, nodes []nodes.Node) error {
	execToPathFn := func(cmd exec.Cmd, path string) func() error {
		return func() error {
			f, err := common.FileOnHost(path)
			if err != nil {
				return err
			}
			defer f.Close()
			return cmd.SetStdout(f).SetStderr(f).Run()
		}
	}
	// construct a slice of methods to collect logs
	fns := []func() error{
		// record info about the host runtime
		execToPathFn(
			exec.Command(binaryName, "system", "status"),
			filepath.Join(dir, "container-info.txt"),
		),
	}
	// inspect each node
	for _, n := range nodes {
		node := n // https://golang.org/doc/faq#closures_and_goroutines
		name := node.String()
		path := filepath.Join(dir, name)
		fns = append(fns,
			execToPathFn(exec.Command(binaryName, "inspect", name), filepath.Join(path, "inspect.json")),
		)
	}
	// run and collect up all errors
	return errors.AggregateConcurrent(fns)
}

// Info returns the provider info.
// Each node is a lightweight VM with its own kernel, the kindest node
// images boot with cgroup v2 and full resource control support.
func (p *provider) Info() (*providers.ProviderInfo, error) {
	return &providers.ProviderInfo{
		Rootless:            false,
		Cgroup2:             true,
		SupportsMemoryLimit: true,
		SupportsPidsLimit:   true,
		SupportsCPUShares:   true,
	}, nil
}
