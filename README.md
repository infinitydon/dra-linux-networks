# DRA Linux Networks

`dra-linux-networks` is a Kubernetes Dynamic Resource Allocation (DRA) driver for
creating Linux `macvlan` and `ipvlan` interfaces in pods without Multus
`NetworkAttachmentDefinition` objects and without CNI secondary attachments.

The driver uses:

- Kubernetes DRA for scheduling and allocation.
- The kubelet DRA plugin API for claim preparation.
- containerd NRI for pod sandbox network namespace attachment.
- Linux netlink for `macvlan` and `ipvlan` creation.

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
- Default parent interface: `enp8s20`

The control-plane node is intentionally excluded by using a node label selector.

## Install

Label the worker node that should advertise Linux network DRA resources:

```bash
kubectl label node ebpf-bng-node-01 linux-net.dra.infinitydon.com/enabled=true
```

Install the chart:

```bash
helm upgrade --install linux-net-dra ./deployments/helm/linux-net-dra \
  --namespace kube-system
```

By default the chart advertises `enp8s20`:

```yaml
interfaces:
  - name: enp8s20
    default: true
    types:
      - macvlan
      - ipvlan
    defaultType: macvlan
    defaultMode: bridge
    defaultPodInterfaceName: net1
    mtu: 9000
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

## Claim Parameters

Workloads pass network intent through DRA opaque configuration:

```yaml
opaque:
  driver: linux-net.dra.infinitydon.com
  parameters:
    type: macvlan
    mode: bridge
    interfaceName: net1
    mtu: 9000
    addresses:
      - 10.55.20.10/24
    gateway: 10.55.20.1
```

Supported fields:

- `type`: `macvlan` or `ipvlan`
- `mode`: macvlan `bridge`, `private`, `vepa`, `passthru`; ipvlan `l2`, `l3`, `l3s`
- `interfaceName`: interface name inside the pod, default `net1`
- `mtu`: pod interface MTU
- `addresses`: static addresses in CIDR notation
- `gateway`: default IPv4 gateway
- `routes`: additional routes with `destination` and `gateway`

## Notes

- This driver is not a Multus replacement for arbitrary CNI chaining. It is a
  focused DRA/NRI driver for Linux parent-link based pod interfaces.
- The parent interface list is explicit in Helm values by design. That avoids
  accidentally advertising management NICs or control-plane interfaces.
- A claim is expected to be reserved by one pod. Shared parent links are fine,
  but each generated pod interface is claim-specific.
- Do not mix `macvlan` and `ipvlan` children on the same Linux parent interface
  at the same time. Linux rejects that with `device or resource busy`. Use one
  parent for macvlan workloads and a different parent for ipvlan workloads if
  both families must run concurrently.
