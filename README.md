# DRA Linux Networks

`dra-linux-networks` is a Kubernetes Dynamic Resource Allocation (DRA) driver for
creating Linux `macvlan` and `ipvlan` interfaces in pods without Multus
`NetworkAttachmentDefinition` objects and without CNI secondary attachments. It
also allocates exclusive host NICs and userspace DPDK PCI devices.

The driver uses:

- Kubernetes DRA for scheduling and allocation.
- The kubelet DRA plugin API for claim preparation.
- containerd NRI for pod sandbox network namespace attachment.
- Linux netlink for `macvlan` and `ipvlan` creation.
- Exclusive whole-NIC assignment with host-device lifecycle restoration.
- Automatic PCI DPDK discovery and CDI-based VFIO/UIO device injection.
- Cluster-scoped `IPAllocation` objects for multi-node IP uniqueness.
- A controller Deployment for stale-allocation cleanup and pool status.

## Status

This is an early implementation intended for lab validation on Kubernetes 1.34+
clusters with `resource.k8s.io/v1` and containerd NRI enabled.

## Cluster Readiness

The target lab cluster was checked before implementation:

- Kubernetes server: `v1.36.2`
- DRA API: `resource.k8s.io/v1`
- Runtime: MicroK8s containerd `v2.2.3`
- NRI: enabled with socket `/var/run/nri/nri.sock`
- Worker node for this driver: `ebpf-bng-node-01`
- Lab test parent interface: `enp8s20`

The control-plane node is intentionally excluded by using a node label selector.

## Install

Versioned source and packaged Helm charts are published on the
[GitHub Releases page](https://github.com/infinitydon/dra-linux-networks/releases).
Container images are published at `ghcr.io/infinitydon/dra-linux-networks`.

Label the worker node that should advertise Linux network DRA resources:

```bash
kubectl label node ebpf-bng-node-01 linux-net.dra.infinitydon.com/enabled=true
```

Create an operator-owned values file that lists only the interfaces which may
be advertised on labeled nodes. A starting example is provided at
`examples/values-netdevices.yaml`; the same complete inventory shape is
documented inline in the chart's `values.yaml`.

Install the chart with that inventory:

```bash
helm upgrade --install linux-net-dra ./deployments/helm/linux-net-dra \
  --namespace kube-system \
  --values my-linux-net-values.yaml
```

Create environment-specific pools separately from the chart release:

```bash
kubectl apply -f examples/ippool-lan-88.yaml
```

The chart defaults to an empty inventory and advertises no host interfaces:

```yaml
interfaces: []
dpdk:
  enabled: false
```

Interface names are node-local operator inventory, not portable chart defaults.
Each NIC is either `shared` for macvlan/ipvlan parents or `exclusive` for
host-device assignment. The two policies cannot be mixed on one physical NIC.
The driver publishes kernel driver, bus type, PCI address/vendor/device IDs,
MAC, MTU, and link state as ResourceSlice attributes for CEL selection.

The example-cluster inventory defines `enp8s20` as a shared parent and
`enp8s21`/`enp8s22` as exclusive host-device NICs. A workload can pin its
macvlan parent with a CEL selector; see
`examples/resourceclaimtemplate-macvlan-specific-parent.yaml`.

A single ResourceClaimTemplate can request several device types. The
`examples/deployment-macvlan-dpdk.yaml` workload creates one claim containing a
macvlan request pinned to `enp8s20` and an Intel VF DPDK request. Its
`matchAttribute: resource.kubernetes.io/pcieRoot` constraint requires both
allocations to share a PCIe root complex.

For PCI-backed kernel interfaces and DPDK functions, the driver publishes the
standard `resource.kubernetes.io/pciBusID` and
`resource.kubernetes.io/pcieRoot` attributes. PCIe roots are resolved from the
sysfs device hierarchy and use Kubernetes' `pci<domain>:<bus>` format. Devices
without resolvable PCI ancestry do not advertise these attributes and cannot
satisfy claims that require PCIe-root alignment.

The same ResourceClaimTemplate may also be referenced more than once by one
Pod. For macvlan and ipvlan attachments, the first available configured name is
kept and collisions are incremented deterministically (`net1`, `net2`, ...).
The `examples/pod-multiple-macvlan.yaml` workload demonstrates two claims from
one reusable template.

A Pod can override an individual attachment by using its `spec.resourceClaims`
name in an annotation:

```yaml
metadata:
  annotations:
    linux-net.dra.infinitydon.com/network-b.interface-name: storage0
spec:
  resourceClaims:
    - name: network-a
      resourceClaimTemplateName: linux-net-reusable-macvlan
    - name: network-b
      resourceClaimTemplateName: linux-net-reusable-macvlan
```

Claim-specific overrides take precedence over the template's `interfaceName`.
Names must be valid Linux interface names of at most 15 bytes and must be
unique inside the Pod.

The chart installs the `IPPool` CRD but does not create any pool instances.
The example pool is operator-owned and contains:

```yaml
apiVersion: linux-net.dra.infinitydon.com/v1alpha1
kind: IPPool
metadata:
  name: lan-88
spec:
  subnet: 192.168.88.0/24
  allocations:
    - name: dynamic
      rangeStart: 192.168.88.11
      rangeEnd: 192.168.88.15
  reservations:
    - name: static
      addresses:
        - 192.168.88.10
  gateway: 192.168.88.1
```

Cluster-wide allocations can be inspected with:

```bash
kubectl get lnipa
```

Each pool/address pair maps to one deterministic `IPAllocation` object. Node
plugins create that object atomically through the Kubernetes API server. If two
workers request the same address concurrently, only one create succeeds and the
other worker tries the next dynamic address or rejects the conflicting static
request.

The controller runs independently of the node plugins. It deletes allocations
whose referenced `ResourceClaim` no longer exists or whose claim UID has changed,
and reports `allocated`, `dynamicAllocated`, and `staticAllocated` counts in
`IPPool.status`. The local node state file is not used for cluster-wide locking.

The chart runs two controller replicas by default. They use a
`coordination.k8s.io/v1` Lease so only the elected leader reconciles allocations;
standby replicas remain ready for failover. Leader-election health is included in
the liveness endpoint. Configuration follows the stable client-go `LeaseLock`
pattern rather than alpha Coordinated Leader Election. See the Kubernetes
[Lease documentation](https://kubernetes.io/docs/concepts/architecture/leases/)
and [client-go leader election API](https://pkg.go.dev/k8s.io/client-go/tools/leaderelection).

## Multi-node test

The repeatable e2e suite pins static and dynamic workloads to separate workers,
checks allocation ownership, gateway reachability, bidirectional secondary-network
traffic, controller status, and resource cleanup:

```bash
KUBECONFIG=/path/to/kubeconfig go test -tags=e2e ./tests/e2e -v -args \
  -static-node ebpf-bng-node-01 \
  -dynamic-node ebpf-bng-node-02 \
  -static-address 192.168.88.10/24 \
  -gateway 192.168.88.1
```

## Example

Create a macvlan claim and pod:

```bash
kubectl apply -f examples/resourceclaimtemplate-macvlan.yaml
kubectl exec -it linux-net-macvlan-test -- ip addr show net1
```

Create an ipvlan claim and pod:

```bash
kubectl apply -f examples/resourceclaimtemplate-ipvlan.yaml
kubectl exec -it linux-net-ipvlan-test -- ip addr show net1
```

Create a three-replica Deployment with one dynamic claim per Pod:

```bash
kubectl apply -f examples/deployment-dynamic-pool.yaml
kubectl get pods -l app=linux-net-dynamic-deployment -o wide
kubectl get lnipa
```

Test workloads pin netshoot to the immutable `v0.15` image digest so repeated
runs use the same userspace tooling.

Assign one whole NIC per Pod from an exclusive pool:

```bash
kubectl apply -f examples/deployment-host-device-pool.yaml
kubectl get pods -l app=linux-net-host-device-pool -o wide
```

The example selects `e1000` devices without naming a particular interface.
DRA allocates different available NICs to each Pod. During deletion, the driver
returns each NIC to the host and restores its original name, MAC, MTU, addresses,
and administrative state. Interfaces carrying a node IP, default route, or link
master are rejected unless the operator explicitly sets `allowUnsafe: true`.

## DPDK devices

DPDK inventory is discovered from PCI sysfs. Operators define safety filters,
not a static PCI address inventory:

```yaml
dpdk:
  enabled: true
  allowUnsafeNoIOMMU: false
  allowedKernelDrivers: [vfio-pci]
  pciClasses: ["0200"]
  include:
    vendors: []
    devices: []
    pciAddresses: []
  exclude:
    pciAddresses: []
  compatibleDriverOverrides:
    "8086:154c": [iavf]
```

When `pciClasses` is omitted, the driver defaults to Ethernet class `0200` as a
safety boundary. An explicit `pciClasses: []` disables PCI class filtering and
allows any device class that also matches the configured userspace drivers and
include/exclude selectors.

The driver publishes each eligible PCI function as an exclusive DRA device and
reports its BDF, numeric IDs, manufacturer/model, current and compatible kernel
drivers, NUMA node, IOMMU group and IOMMU mode. `allowedKernelDrivers` is the
allow-list for currently bound host kernel drivers that are considered
DPDK-eligible. PCI addresses are optional
include/exclude filters. Kernel driver candidates come from the device modalias
and the host's `modules.alias`; overrides handle ambiguous devices.

VFIO allocation is conservative: a function is published only when it is the
sole member of its IOMMU group. This prevents separate claims from sharing one
DMA-isolation boundary. Devices whose group contains multiple functions are
withheld until whole-group allocation is implemented.

During claim preparation the driver writes a claim-specific CDI specification.
Kubelet passes its CDI ID to the runtime, which injects `/dev/vfio/vfio` and the
allocated group device. No network namespace interface is created. DPDK requests
normally do not need opaque claim configuration; if configuration is supplied,
the driver rejects IPAM, gateway, route, interface-name and MTU fields.

Safe IOMMU-backed VFIO is the default. VFIO no-IOMMU devices are advertised only
with `allowUnsafeNoIOMMU: true`; this provides no DMA isolation and should be
limited to explicitly trusted lab nodes. The driver maps host
`/dev/vfio/noiommu-N` to `/dev/vfio/N` inside the workload.

Run the project-owned testpmd workload after enabling DPDK discovery:

```bash
kubectl apply -f examples/deployment-dpdk-testpmd.yaml
kubectl logs -l app=linux-net-dpdk-testpmd
```

Run two independent VPP 25.10 instances, each with its own generated claim,
exclusive Intel VF, and deterministic VPP interface address:

```bash
kubectl apply -f examples/statefulset-dpdk-vpp-pair.yaml
kubectl get pods -l app=linux-net-dpdk-vpp -o wide
kubectl exec linux-net-dpdk-vpp-0 -- \
  vppctl -s /run/vpp/cli.sock ping 192.168.88.21 repeat 3
kubectl exec linux-net-dpdk-vpp-1 -- \
  vppctl -s /run/vpp/cli.sock ping 192.168.88.20 repeat 3
```

The VPP example pins
[`ligato/vpp-base:25.10-release`](https://hub.docker.com/r/ligato/vpp-base)
by digest. Each replica requests two CPUs and two 1 GiB hugepages. The e2e test
verifies distinct PCI addresses and IOMMU groups, CDI-only VFIO nodes, VPP
version, Intel iAVF hardware discovery, and bidirectional VPP traffic between
`192.168.88.20/24` and `192.168.88.21/24`.

A single Pod can request multiple DPDK functions with the Kubernetes 1.36+
DRA-backed extended-resource API. The scalar value is the number of devices:

```yaml
resources:
  limits:
    deviceclass.resource.kubernetes.io/linux-net-dpdk-intel-vf: 2
```

The DeviceClass holds the reusable driver and hardware selectors. The scheduler
creates one Pod-owned ResourceClaim with `ExactCount: 2`; no
ResourceClaimTemplate is required. This example defines an Intel VF class and
starts one VPP instance with two matching devices:

```bash
kubectl apply -f examples/pod-dpdk-vpp-multi-device.yaml
kubectl exec linux-net-dpdk-vpp-multi -- \
  vppctl -s /run/vpp/cli.sock show hardware-interfaces
```

Every allocated function contributes a unique
`LINUX_NET_DRA_PCI_ADDRESS_PCI_*` and `LINUX_NET_DRA_IOMMU_GROUP_PCI_*`
environment variable. The singular variables remain available for
single-device workloads. ResourceClaim and Pod network status contain one
entry per allocated PCI function.

The example requests two 1 GiB hugepages and uses the versioned
`dra-linux-networks-dpdk-testpmd` image, built from DPDK v26.03. Hugepage provisioning, CPU isolation and
binding devices to a userspace driver remain node-administration concerns; the
driver intentionally does not rebind PCI devices.

## Claim Parameters

Workloads pass macvlan, ipvlan, and host-device network intent through DRA
opaque configuration:

```yaml
opaque:
  driver: linux-net.dra.infinitydon.com
  parameters:
    type: macvlan
    mode: bridge
    interfaceName: net1
    mtu: 9000
    ipPool: lan-88
```

Supported fields:

- `type`: `macvlan`, `ipvlan`, or `host-device`
- `mode`: macvlan `bridge`, `private`, `vepa`, `passthru`; ipvlan `l2`, `l3`, `l3s`
- `interfaceName`: interface name inside the pod, default `net1`
- `mtu`: pod interface MTU
- `ipPool`: named `IPPool`
- `address`: optional static address from that pool, for example `192.168.88.10/24`
- `addresses`: direct static addresses in CIDR notation, mostly for testing or advanced use
- `gateway`: default IPv4 gateway
- `routes`: additional routes with `destination` and `gateway`

DPDK requests are selected through the `DeviceClass`, CEL selectors, count, and
constraints. They omit opaque configuration unless a future DPDK-specific option
is added.

When `ipPool` is set and `address` is omitted, the driver reserves the next
free address from `spec.allocations`, skipping any address in `spec.reservations`.
When a static `address` is set, the driver only accepts it if it is inside
`spec.reservations`.

To keep a single reusable `ResourceClaimTemplate`, put static IP requests on
the Pod instead of the template:

```yaml
metadata:
  annotations:
    linux-net.dra.infinitydon.com/net1.ip-pool: lan-88
    linux-net.dra.infinitydon.com/net1.address: 192.168.88.10/24
```

The annotation key may use either the DRA request name or the pod interface
name. The `ip-pool` annotation is required with `address` and must match the
`ipPool` in the ResourceClaim configuration. The requested IP must be covered
by that `IPPool` reservation. Missing or mismatched pool references are rejected.

## Network status

The driver reports durable network state in the generated ResourceClaim under
`status.devices`. A successfully attached device includes a `Ready=True`
condition, `networkData.interfaceName`, assigned CIDR addresses, hardware
address, and driver data containing the IPPool, link type, mode, and parent.

The same attached state is summarized persistently on the Pod in the
`linux-net.dra.infinitydon.com/network-status` annotation. Its JSON array
contains the interface, addresses, MAC, IPPool, gateway, parent, link type and
mode, ResourceClaim reference, and attachment state, so `kubectl describe pod`
continues to show the network after Events expire. The JSON is stored with
multiline indentation so `kubectl describe pod` keeps each field readable.
DPDK entries omit IP and interface fields and include PCI manufacturer/model,
driver candidates, NUMA node, IOMMU details, device nodes and the CDI device ID.

The driver also emits `LinuxNetworkPrepared`, `LinuxNetworkAttached`, and
`LinuxNetworkAttachFailed` Events against the Pod, so the lifecycle appears in
`kubectl describe pod`.

DRA preparation and CDI injection complete synchronously before workload
containers are created. The NRI `RunPodSandbox` attachment hook is synchronous
with sandbox creation. Therefore the examples use normal Kubernetes Pod
readiness and do not require a custom readiness gate. ResourceClaim status is
the DRA-native source of truth, the Pod annotation is its durable Pod-level
summary, and Events are supplemental records that expire according to the
cluster Event retention policy.

The driver retains backward-compatible support for the optional
`linux-net.dra.infinitydon.com/NetworkReady` gate, but it is only appropriate
when an operator deliberately wants that additional custom condition.

Pool `gateway` is not automatically installed as a second default route because
pods normally already have a default route from the primary CNI. Add an explicit
claim `gateway` or `routes` entry when the secondary interface should own routes.

## Notes

- This driver is not a Multus replacement for arbitrary CNI chaining. It is a
  focused DRA/NRI driver for Linux parent-link based pod interfaces.
- The parent interface list is explicit in Helm values by design. That avoids
  accidentally advertising management NICs or control-plane interfaces.
- A claim is expected to be reserved by one pod. Shared parent links are fine,
  but each generated pod interface is claim-specific.
- Multiple workers may use the same `IPPool`; allocation uniqueness is enforced
  by cluster-scoped `IPAllocation` resources.
- Do not mix `macvlan` and `ipvlan` children on the same Linux parent interface
  at the same time. Linux rejects that with `device or resource busy`. Use one
  parent for macvlan workloads and a different parent for ipvlan workloads if
  both families must run concurrently.
