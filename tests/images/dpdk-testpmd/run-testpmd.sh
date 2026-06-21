#!/bin/sh
set -eu

: "${LINUX_NET_DRA_PCI_ADDRESS:?DRA did not inject the PCI address}"
: "${LINUX_NET_DRA_IOMMU_GROUP:?DRA did not inject the IOMMU group}"

test -c /dev/vfio/vfio
test -c "/dev/vfio/${LINUX_NET_DRA_IOMMU_GROUP}"

echo "Starting testpmd with PCI device ${LINUX_NET_DRA_PCI_ADDRESS}, IOMMU group ${LINUX_NET_DRA_IOMMU_GROUP}"
DPDK_LCORES="${DPDK_LCORES:-$(awk '/^Cpus_allowed_list:/ { print $2 }' /proc/self/status)}"
echo "Using Kubernetes CPU set ${DPDK_LCORES}"
tail -f /dev/null | exec dpdk-testpmd \
  -l "${DPDK_LCORES}" \
  -n "${DPDK_MEMORY_CHANNELS:-2}" \
  --iova-mode="${DPDK_IOVA_MODE:-pa}" \
  -a "${LINUX_NET_DRA_PCI_ADDRESS}" \
  -- \
  --forward-mode=io \
  --auto-start \
  --stats-period=5
