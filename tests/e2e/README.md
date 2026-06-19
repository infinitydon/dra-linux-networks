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
