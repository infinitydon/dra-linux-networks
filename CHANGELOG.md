# Changelog

## Unreleased

- Pin netshoot test workloads to the immutable `v0.15` image digest.
- Add a three-replica dynamic-allocation Deployment example.

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
