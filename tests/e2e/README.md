# Multi-node e2e tests

Run the test against two labeled worker nodes:

```bash
KUBECONFIG=/path/to/kubeconfig go test -tags=e2e ./tests/e2e -v -args \
  -static-node ebpf-bng-node-01 \
  -dynamic-node ebpf-bng-node-02 \
  -static-address 192.168.88.10/24 \
  -gateway 192.168.88.1
```

Set `KUBECTL` when `kubectl` is not on `PATH`. The test verifies pinned static
and dynamic allocation, cluster allocation ownership, gateway reachability,
bidirectional cross-node connectivity, controller status, and cleanup.

The equivalent Make target is:

```bash
make test-e2e-multi-node \
  STATIC_NODE=ebpf-bng-node-01 \
  DYNAMIC_NODE=ebpf-bng-node-02
```

Run the exclusive host-device pool lifecycle test on the worker with the
configured NIC list:

```bash
KUBECONFIG=/path/to/kubeconfig go test -tags=e2e ./tests/e2e -v -args \
  -host-device-node ebpf-bng-node-02
```

Run the DPDK discovery, CDI injection, status, and testpmd startup test on a
worker with VFIO devices and at least two 1 GiB hugepages:

```bash
go test -tags=e2e ./tests/e2e -v -args \
  -dpdk-node ebpf-bng-node-01
```

VFIO no-IOMMU devices require `dpdk.allowUnsafeNoIOMMU=true` in the Helm
release. This mode does not provide DMA isolation and is intended only for
explicitly trusted lab nodes.

Run two independent VPP 25.10 instances and verify that DRA assigns a distinct
Intel VF and VFIO group to each Pod:

```bash
go test -tags=e2e ./tests/e2e -v \
  -run TestTwoVPPInstancesReceiveExclusiveDPDKDevices -args \
  -dpdk-node ebpf-bng-node-01
```

The test assigns two different physical NICs, verifies a third Pod cannot
schedule, checks name and administrative-state restoration, and then verifies
the released NIC can be allocated again.
