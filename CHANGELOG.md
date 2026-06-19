# Changelog

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
