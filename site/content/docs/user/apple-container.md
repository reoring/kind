---
title: "Apple container"
menu:
  main:
    parent: "user"
    identifier: "apple-container"
    weight: 3
---
kind has experimental support for [Apple container](https://github.com/apple/container)
as a node provider on macOS. Apple container runs each container as a
lightweight virtual machine with its own Linux kernel, so each kind node is an
isolated VM rather than a container sharing the host kernel.

## Provider requirements

- macOS 26 or later on Apple silicon
- Apple container CLI 1.0 or later, with its services running
  (`container system start`)

## Creating a cluster

The provider is selected with the `KIND_EXPERIMENTAL_PROVIDER` environment
variable:

{{< codeFromInline lang="bash" >}}
KIND_EXPERIMENTAL_PROVIDER=container kind create cluster
{{< /codeFromInline >}}

If neither docker, podman, nor nerdctl are installed, kind will also
auto-detect the `container` CLI.

## Node resources

Apple container allocates each VM 1GiB of memory by default, which is not
enough to run a Kubernetes node, so the provider defaults each node to 4GiB of
memory and 4 CPUs. Use these environment variables to override the defaults at
cluster creation time:

{{< codeFromInline lang="bash" >}}
KIND_EXPERIMENTAL_APPLE_CONTAINER_MEMORY=8G \
KIND_EXPERIMENTAL_APPLE_CONTAINER_CPUS=6 \
KIND_EXPERIMENTAL_PROVIDER=container kind create cluster
{{< /codeFromInline >}}

Note that every node in a multi-node cluster reserves this amount of memory.

## Multi-node clusters and restarts

Apple container assigns containers new IP addresses every time they start.
A kind node repairs references to its own address at boot, but in a
multi-node cluster the workers also need to find the control-plane after its
address changed. There are two modes:

- **Default (no DNS domain configured)**: the provider embeds the
  control-plane IP into the cluster configuration. The cluster works until
  its node VMs are stopped — after a restart (including a host reboot or
  `container system stop`), the cluster breaks and must be recreated:
  workers can no longer reach the API server, and even on single-node
  clusters the kubeadm-generated configuration stored inside the cluster
  (e.g. the kube-proxy kubeconfig) still points at the old address, so
  pods lose access to the API service and CoreDNS stops reporting ready,
  even though `kubectl` from the host keeps working.

- **With a local DNS domain**: the provider uses node hostnames such as
  `kind-control-plane.<domain>` instead of IPs. Hostnames track address
  changes, so multi-node clusters survive node restarts.

To set up the local DNS domain, register a domain (administrator privileges
are required):

{{< codeFromInline lang="bash" >}}
sudo container system dns create kind
{{< /codeFromInline >}}

then configure it as the default DNS domain in
`~/.config/container/config.toml`:

```toml
[dns]
domain = "kind"
```

and restart the Apple container services:

{{< codeFromInline lang="bash" >}}
container system stop && container system start
{{< /codeFromInline >}}

Both steps are required: `container system dns create` only registers the
domain with the macOS resolver, and container hostnames are only served under
the default domain from the runtime configuration. The `--dns-domain` flag of
`container run` does not enable this — it only writes the container's
`resolv.conf`.

The provider detects the domain when a cluster is created, so clusters
created before the DNS domain was configured remain IP-based and need to be
recreated to benefit.

Note that Apple container has no restart policy: after a host reboot, start
each node VM manually (one at a time — `container start` accepts a single
container) and the cluster will recover:

{{< codeFromInline lang="bash" >}}
container start kind-control-plane
container start kind-worker
{{< /codeFromInline >}}

## High-availability clusters

Clusters with multiple control-plane nodes require the local DNS domain
described above — the external load balancer (envoy) resolves the
control-plane nodes by name and re-resolves them when their addresses
change. Without the domain configured, creating an HA cluster fails with an
error explaining the setup.

API server failover works as on other providers: stopping a control-plane
node removes it from the load balancer rotation and `kubectl` keeps working
through the remaining control-planes.

Restarting a control-plane node is where this provider differs: the node
comes back with a new IP address, and while the load balancer follows the
change automatically, etcd does not — the cluster's member record still
holds the old peer address, and the node's etcd certificates do not cover
the new address. The cluster stays available (quorum permitting), but the
restarted control-plane stays `NotReady` until you repair it:

{{< codeFromInline lang="bash" >}}
# 1. find the member ID and update its peer URL (run on a healthy control-plane)
container exec <healthy-control-plane> sh -c 'ETCD=$(crictl ps --name etcd -q); crictl exec $ETCD etcdctl \
  --endpoints=https://127.0.0.1:2379 \
  --cacert=/etc/kubernetes/pki/etcd/ca.crt \
  --cert=/etc/kubernetes/pki/etcd/server.crt \
  --key=/etc/kubernetes/pki/etcd/server.key member list'
container exec <healthy-control-plane> sh -c '... member update <member-id> --peer-urls=https://<new-ip>:2380'

# 2. regenerate the etcd certificates on the restarted node and restart etcd
container exec <restarted-control-plane> sh -c 'rm /etc/kubernetes/pki/etcd/peer.* /etc/kubernetes/pki/etcd/server.* && \
  kubeadm init phase certs etcd-peer && kubeadm init phase certs etcd-server && \
  crictl rm -f $(crictl ps --name etcd -q)'
{{< /codeFromInline >}}

The node returns to `Ready` within a couple of minutes.

## Loading images

`kind load docker-image` requires the docker CLI and does not work with this
provider. Use an image archive instead, and save it for the node platform:

{{< codeFromInline lang="bash" >}}
container image save --platform linux/arm64 docker.io/library/my-image:tag --output my-image.tar
KIND_EXPERIMENTAL_PROVIDER=container kind load image-archive my-image.tar
{{< /codeFromInline >}}

## Known limitations

- High-availability clusters require the local DNS domain, and a restarted
  control-plane node needs the manual etcd repair described above.
- `kind build node-image` requires docker and cannot be used with this
  provider.
- `extraMounts` are virtiofs shares: mount propagation and SELinux options
  are ignored.
- SCTP port mappings are not supported.
- Only `linux/arm64` node images run natively; this matches the published
  `kindest/node` images on Apple silicon.
