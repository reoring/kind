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
  `container system stop`), worker nodes can no longer reach the API server
  and the cluster must be recreated. Single-node clusters are unaffected and
  survive restarts.

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

## Loading images

`kind load docker-image` requires the docker CLI and does not work with this
provider. Use an image archive instead, and save it for the node platform:

{{< codeFromInline lang="bash" >}}
container image save --platform linux/arm64 docker.io/library/my-image:tag --output my-image.tar
KIND_EXPERIMENTAL_PROVIDER=container kind load image-archive my-image.tar
{{< /codeFromInline >}}

## Known limitations

- High-availability clusters (multiple control-plane nodes) are not yet
  supported.
- `kind build node-image` requires docker and cannot be used with this
  provider.
- `extraMounts` are virtiofs shares: mount propagation and SELinux options
  are ignored.
- SCTP port mappings are not supported.
- Only `linux/arm64` node images run natively; this matches the published
  `kindest/node` images on Apple silicon.
