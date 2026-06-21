# Changelog

## v0.3.5

- Use the Kubernetes 1.36+ DRA-backed extended-resource API for simple DPDK device counts.
- Request two Intel VF DPDK devices directly from the VPP Pod, using DeviceClass selectors without a ResourceClaimTemplate.
- Verify the scheduler-generated claim, two CDI devices, and VPP hardware initialization in e2e tests.

## v0.3.4

- Add deterministic VPP interface addresses and bidirectional ping validation to the two-instance DPDK example.
- Support collision-free CDI environment metadata for multiple DPDK functions in one container.
- Preserve every allocated PCI function in ResourceClaim and Pod network status.
- Add an `ExactCount: 2` VPP example and e2e coverage for multi-device DPDK claims.

## v0.3.3

- Treat omitted `pciClasses` as the safe Ethernet `0200` default.
- Treat explicit `pciClasses: []` as no PCI class filter.
- Add a digest-pinned two-replica Ligato VPP 25.10 DPDK example.
- Add an e2e test proving exclusive PCI and VFIO-group injection into both VPP instances.

## v0.3.2

- Remove lab-specific interface names from the public Helm defaults.
- Disable DPDK discovery by default and allow an empty device inventory.
- Require operators to supply node-local NIC inventory in a separate values file.
- Add a generic operator values example for shared and exclusive netdevices.

## v0.3.1

- Remove the custom `NetworkReady` readiness gate from standard examples and e2e workloads.
- Use normal Kubernetes Pod readiness with DRA ResourceClaim status as the device source of truth.
- Avoid Pod condition API operations when workloads do not explicitly request the legacy gate.
- Verify gate-free CDI/DPDK and multi-node macvlan lifecycles in the live e2e suite.

## v0.3.0

- Discover VFIO and UIO network-class PCI devices directly from sysfs.
- Publish exclusive DPDK devices with PCI identity, driver, NUMA and IOMMU attributes.
- Resolve compatible kernel drivers through modalias data with operator overrides.
- Inject userspace devices through claim-specific CDI specifications without CNI or IPAM.
- Reject shared VFIO IOMMU groups and require explicit opt-in for unsafe no-IOMMU mode.
- Add detailed ResourceClaim and multiline Pod DPDK status.
- Add a project-owned testpmd image, Deployment example and DPDK injection e2e test.

## v0.2.0

- Discover and publish NIC kernel driver, bus, PCI identity, and link state.
- Add CEL-selectable macvlan, ipvlan, and host-device DeviceClasses.
- Add exclusive host-device NIC pools with persistent attachment state.
- Restore host-device name, MAC, MTU, addresses, and administrative state.
- Add StopPodSandbox, RemovePodSandbox, unprepare, and restart recovery paths.
- Report hardware identity, allocation policy, and lifecycle in claim and Pod status.
- Reject unsafe host-device assignment of node-address, default-route, or mastered NICs.
- Add a two-NIC host-device Deployment example and lifecycle e2e test.

## v0.1.12

- Pin netshoot test workloads to the immutable `v0.15` image digest.
- Add a three-replica dynamic-allocation Deployment example.
- Pretty-print the persistent Pod network-status annotation for readable output.

## v0.1.11

- Persist attached secondary-network details in a driver-owned Pod annotation.
- Extend status tests to cover durable Pod-level reporting after Events expire.

## v0.1.10

- Report DRA-native network state in `ResourceClaim.status.devices`.
- Include interface name, CIDRs, MAC address, IPPool, link type, mode, and parent.
- Emit Pod Events for network preparation, attachment, and attachment failures.
- Add opt-in `linux-net.dra.infinitydon.com/NetworkReady` readiness-gate support.
- Extend multi-node e2e coverage to assert status, Events, and readiness.
- Remove the overlapping bidirectional `/run` mount that could leak propagated mounts.

## v0.1.9

- Move the example `lan-88` IPPool out of the Helm release.
- Make IPPool instances explicitly operator-managed resources.
- Run two controller replicas by default with Lease-based leader election.
- Add leader-election health checks, namespaced RBAC, replica spreading, and a PDB.

## v0.1.8

- Require static Pod annotations to include an explicit `IPPool` reference.
- Reject missing or mismatched Pod pool annotations during claim preparation.
- Add repeatable multi-node static/dynamic IPAM connectivity tests.
- Include the cluster-wide `IPAllocation` controller introduced in `v0.1.7`.

## v0.1.7

- Add cluster-scoped `IPAllocation` resources for multi-node uniqueness.
- Add the allocation reconciliation controller and `IPPool` status reporting.
- Add stale-allocation garbage collection.

## v0.1.6

- Add the cluster-scoped `IPPool` CRD.
- Add dynamic allocation ranges and static reservation ranges.
- Add Pod-level static address selection.
