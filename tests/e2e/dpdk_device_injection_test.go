//go:build e2e

package e2e

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
)

var (
	dpdkNode  = flag.String("dpdk-node", "", "node with DPDK devices and hugepages")
	dpdkImage = flag.String("dpdk-image", "ghcr.io/infinitydon/dra-linux-networks-dpdk-testpmd:0.3.0@sha256:4acf17c1e164eabf9771d8959ad9176c860fa83104af9261875bee0903209665", "pinned DPDK testpmd image")
)

const (
	dpdkClaimTemplate = "linux-net-e2e-dpdk"
	dpdkPod           = "linux-net-e2e-dpdk-testpmd"
)

func TestDPDKDeviceInjectionWithTestPMD(t *testing.T) {
	if *dpdkNode == "" {
		t.Skip("-dpdk-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "pod", dpdkPod, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", dpdkClaimTemplate, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}
	if output, err := kubectl([]byte(dpdkManifest(*dpdkNode, *dpdkImage)), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply DPDK test: %v\n%s", err, output)
	}
	waitReady(t, dpdkPod)
	assertPodNode(t, dpdkPod, *dpdkNode)

	t.Run("PodUsesStandardKubernetesReadiness", func(t *testing.T) {
		customStatus := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", dpdkPod,
			"-o", `jsonpath={.status.conditions[?(@.type=="linux-net.dra.infinitydon.com/NetworkReady")].status}`))
		if customStatus != "" {
			t.Fatalf("unexpected custom NetworkReady condition %q", customStatus)
		}
	})

	t.Run("VFIODeviceNodesAndMetadataAreInjected", func(t *testing.T) {
		output := mustKubectl(t, "-n", *namespace, "exec", dpdkPod, "--", "sh", "-ec", `test -c /dev/vfio/vfio; test -c /dev/vfio/$LINUX_NET_DRA_IOMMU_GROUP; printf '%s %s' "$LINUX_NET_DRA_PCI_ADDRESS" "$LINUX_NET_DRA_IOMMU_GROUP"`)
		fields := strings.Fields(output)
		if len(fields) != 2 || !strings.Contains(fields[0], ":") {
			t.Fatalf("injected DPDK metadata = %q", output)
		}
	})

	t.Run("PodStatusIncludesDetailedPCIIdentity", func(t *testing.T) {
		raw := mustKubectl(t, "-n", *namespace, "get", "pod", dpdkPod, "-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
		var statuses []map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &statuses); err != nil || len(statuses) != 1 {
			t.Fatalf("decode DPDK status: %v, value %s", err, raw)
		}
		for _, field := range []string{"pciAddress", "pciVendorID", "pciDeviceID", "kernelDriver", "iommuMode", "iommuGroup", "cdiDeviceID"} {
			if fmt.Sprint(statuses[0][field]) == "" {
				t.Fatalf("DPDK status is missing %s: %s", field, raw)
			}
		}
	})

	t.Run("ResourceClaimReportsInjectedDevice", func(t *testing.T) {
		claim := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", dpdkPod, "-o", "jsonpath={.status.resourceClaimStatuses[0].resourceClaimName}"))
		status := mustKubectl(t, "-n", *namespace, "get", "resourceclaim", claim, "-o", `jsonpath={.status.devices[0].conditions[?(@.type=="Ready")].status} {.status.devices[0].data.type} {.status.devices[0].data.pciAddress} {.status.devices[0].data.kernelDriver} {.status.devices[0].data.iommuMode}`)
		assertOutputContains(t, status, "True", "dpdk", "vfio")
	})

	t.Run("TestPMDStartsWithAllocatedDevice", func(t *testing.T) {
		logs := mustKubectl(t, "-n", *namespace, "logs", dpdkPod)
		assertOutputContains(t, logs, "Starting testpmd with PCI device", "EAL:")
	})
}

func dpdkManifest(node, image string) string {
	return fmt.Sprintf(`apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: %[1]s
spec:
  spec:
    devices:
      requests:
        - name: dpdk
          exactly:
            deviceClassName: linux-net-dpdk
            selectors:
              - cel:
                  expression: >-
                    device.attributes['linux-net.dra.infinitydon.com'].pciVendorID == '8086' &&
                    device.attributes['linux-net.dra.infinitydon.com'].pciDeviceID == '154c'
      config:
        - requests: ["dpdk"]
          opaque:
            driver: linux-net.dra.infinitydon.com
            parameters:
              type: dpdk
---
apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[3]s
  resourceClaims:
    - name: dpdk
      resourceClaimTemplateName: %[1]s
  containers:
    - name: testpmd
      image: %[4]s
      securityContext:
        capabilities:
          add: ["IPC_LOCK", "SYS_ADMIN", "SYS_RAWIO"]
      resources:
        requests:
          cpu: "2"
          memory: 512Mi
          hugepages-1Gi: 2Gi
        limits:
          cpu: "2"
          memory: 512Mi
          hugepages-1Gi: 2Gi
        claims:
          - name: dpdk
      volumeMounts:
        - name: hugepages
          mountPath: /dev/hugepages
  volumes:
    - name: hugepages
      emptyDir:
        medium: HugePages
`, dpdkClaimTemplate, dpdkPod, node, image)
}
