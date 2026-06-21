//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	vppClaimTemplate = "linux-net-e2e-dpdk-vpp"
	vppDeployment    = "linux-net-e2e-dpdk-vpp"
	vppImage         = "ligato/vpp-base:25.10-release@sha256:07014580a680b41f1fe21b7497a01ff28bbceb6d23eecfe0dc3174e23eb69541"
)

type vppDPDKStatus struct {
	PCIAddress   string `json:"pciAddress"`
	IOMMUGroup   string `json:"iommuGroup"`
	IOMMUMode    string `json:"iommuMode"`
	KernelDriver string `json:"kernelDriver"`
	State        string `json:"state"`
}

func TestTwoVPPInstancesReceiveExclusiveDPDKDevices(t *testing.T) {
	if *dpdkNode == "" {
		t.Skip("-dpdk-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "deployment", vppDeployment, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", vppClaimTemplate, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}
	if output, err := kubectl([]byte(vppPairManifest(*dpdkNode)), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply VPP pair: %v\n%s", err, output)
	}
	mustKubectl(t, "-n", *namespace, "rollout", "status", "deployment/"+vppDeployment, "--timeout="+*waitTimeout)

	podOutput := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pods", "-l", "app="+vppDeployment, "-o", "jsonpath={.items[*].metadata.name}"))
	pods := strings.Fields(podOutput)
	if len(pods) != 2 {
		t.Fatalf("VPP Pod count = %d, want 2: %s", len(pods), podOutput)
	}

	addresses := map[string]bool{}
	groups := map[string]bool{}
	for _, pod := range pods {
		assertPodNode(t, pod, *dpdkNode)
		status := vppReportedDPDKStatus(t, pod)
		if status.PCIAddress == "" || status.IOMMUGroup == "" || status.KernelDriver != "vfio-pci" || status.State != "Injected" {
			t.Fatalf("Pod %s has incomplete DPDK status: %+v", pod, status)
		}
		addresses[status.PCIAddress] = true
		groups[status.IOMMUGroup] = true

		output := mustKubectl(t, "-n", *namespace, "exec", pod, "--", "sh", "-ec", `
test -c /dev/vfio/vfio
test -c "/dev/vfio/$LINUX_NET_DRA_IOMMU_GROUP"
test "$(find /dev/vfio -maxdepth 1 -type c | wc -l)" -eq 2
vppctl -s /run/vpp/cli.sock show version
vppctl -s /run/vpp/cli.sock show hardware-interfaces
`)
		assertOutputContains(t, output, "vpp v25.10-release", "VirtualFunctionEthernet", "Intel iAVF", "pci: device 8086:154c")

		claim := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", pod, "-o", "jsonpath={.status.resourceClaimStatuses[0].resourceClaimName}"))
		claimStatus := mustKubectl(t, "-n", *namespace, "get", "resourceclaim", claim,
			"-o", `jsonpath={.status.devices[0].conditions[?(@.type=="Ready")].status} {.status.devices[0].data.pciAddress} {.status.devices[0].data.iommuGroup}`)
		assertOutputContains(t, claimStatus, "True", status.PCIAddress, status.IOMMUGroup)
	}
	if len(addresses) != 2 || len(groups) != 2 {
		t.Fatalf("VPP instances did not receive exclusive devices: addresses=%v groups=%v", addresses, groups)
	}
}

func vppReportedDPDKStatus(t *testing.T, pod string) vppDPDKStatus {
	t.Helper()
	raw := mustKubectl(t, "-n", *namespace, "get", "pod", pod, "-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
	var statuses []vppDPDKStatus
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil || len(statuses) != 1 {
		t.Fatalf("decode Pod %s DPDK status: %v, value %s", pod, err, raw)
	}
	return statuses[0]
}

func vppPairManifest(node string) string {
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
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[2]s
spec:
  replicas: 2
  selector:
    matchLabels:
      app: %[2]s
  template:
    metadata:
      labels:
        app: %[2]s
    spec:
      nodeSelector:
        kubernetes.io/hostname: %[3]s
      resourceClaims:
        - name: dpdk
          resourceClaimTemplateName: %[1]s
      containers:
        - name: vpp
          image: %[4]s
          command: ["/bin/sh", "-ec"]
          args:
            - |
              cat >/tmp/vpp-startup.conf <<EOF
              unix {
                nodaemon
                nobanner
                cli-listen /run/vpp/cli.sock
              }
              api-segment { gid 0 }
              cpu {
                relative
                main-core 0
                workers 1
              }
              dpdk {
                dev ${LINUX_NET_DRA_PCI_ADDRESS}
                uio-driver vfio-pci
                no-multi-seg
              }
              EOF
              exec vpp -c /tmp/vpp-startup.conf
          securityContext:
            capabilities:
              add: ["IPC_LOCK", "SYS_ADMIN", "SYS_RAWIO"]
          readinessProbe:
            exec:
              command: ["vppctl", "-s", "/run/vpp/cli.sock", "show", "version"]
            initialDelaySeconds: 5
            periodSeconds: 5
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
            - name: run
              mountPath: /run/vpp
      volumes:
        - name: hugepages
          emptyDir:
            medium: HugePages
        - name: run
          emptyDir: {}
`, vppClaimTemplate, vppDeployment, node, vppImage)
}
