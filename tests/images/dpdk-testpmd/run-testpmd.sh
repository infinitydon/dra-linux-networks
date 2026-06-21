#!/bin/sh
set -eu

: "${LINUX_NET_DRA_PCI_ADDRESS:?DRA did not inject the PCI address}"
: "${LINUX_NET_DRA_IOMMU_GROUP:?DRA did not inject the IOMMU group}"

test -c /dev/vfio/vfio
test -c "/dev/vfio/${LINUX_NET_DRA_IOMMU_GROUP}"

echo "Starting testpmd with PCI device ${LINUX_NET_DRA_PCI_ADDRESS}, IOMMU group ${LINUX_NET_DRA_IOMMU_GROUP}"
tail -f /dev/null | exec dpdk-testpmd \
  -l "${DPDK_LCORES:-0-1}" \
  -n "${DPDK_MEMORY_CHANNELS:-2}" \
  --iova-mode=va \
  -a "${LINUX_NET_DRA_PCI_ADDRESS}" \
  -- \
  --forward-mode=io \
  --auto-start \
  --stats-period=5
