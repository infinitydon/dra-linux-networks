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
	vppMultiClass    = "linux-net-e2e-dpdk-vpp-multi"
	vppMultiPod      = "linux-net-e2e-dpdk-vpp-multi"
)

type vppDPDKStatus struct {
	PCIAddress   string `json:"pciAddress"`
	IOMMUGroup   string `json:"iommuGroup"`
	IOMMUMode    string `json:"iommuMode"`
	KernelDriver string `json:"kernelDriver"`
	State        string `json:"state"`
}

func TestSingleVPPReceivesTwoDPDKDevices(t *testing.T) {
	if *dpdkNode == "" {
		t.Skip("-dpdk-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "pod", vppMultiPod, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "delete", "deviceclass", vppMultiClass, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}
	if output, err := kubectl([]byte(vppMultiDeviceManifest(*dpdkNode)), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply multi-device VPP: %v\n%s", err, output)
	}
	waitReady(t, vppMultiPod)
	assertPodNode(t, vppMultiPod, *dpdkNode)

	raw := mustKubectl(t, "-n", *namespace, "get", "pod", vppMultiPod, "-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
	var statuses []vppDPDKStatus
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil || len(statuses) != 2 {
		t.Fatalf("multi-device Pod status count = %d, error = %v, value = %s", len(statuses), err, raw)
	}
	if statuses[0].PCIAddress == statuses[1].PCIAddress || statuses[0].IOMMUGroup == statuses[1].IOMMUGroup {
		t.Fatalf("multi-device Pod received duplicate resources: %+v", statuses)
	}

	output := mustKubectl(t, "-n", *namespace, "exec", vppMultiPod, "--", "sh", "-ec", `
test "$(find /dev/vfio -maxdepth 1 -type c | wc -l)" -eq 3
test "$(env | grep -c '^LINUX_NET_DRA_PCI_ADDRESS_PCI_')" -eq 2
env | grep '^LINUX_NET_DRA_PCI_ADDRESS_PCI_' | sort
vppctl -s /run/vpp/cli.sock show hardware-interfaces
`)
	if strings.Count(output, "Intel iAVF") != 2 || strings.Count(output, "pci: device 8086:154c") != 2 {
		t.Fatalf("VPP did not initialize two Intel VFs:\n%s", output)
	}

	claim := extendedResourceClaimForPod(t, vppMultiPod)
	claimStatus := mustKubectl(t, "-n", *namespace, "get", "resourceclaim", claim,
		"-o", `jsonpath={range .status.devices[*]}{.conditions[?(@.type=="Ready")].status}{" "}{.data.pciAddress}{"\n"}{end}`)
	if strings.Count(claimStatus, "True") != 2 {
		t.Fatalf("ResourceClaim does not report two ready devices:\n%s", claimStatus)
	}
}

func extendedResourceClaimForPod(t *testing.T, pod string) string {
	t.Helper()
	podUID := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", pod, "-o", "jsonpath={.metadata.uid}"))
	raw := mustKubectl(t, "-n", *namespace, "get", "resourceclaims", "-o", "json")
	var claims struct {
		Items []struct {
			Metadata struct {
				Name            string            `json:"name"`
				Annotations     map[string]string `json:"annotations"`
				OwnerReferences []struct {
					UID string `json:"uid"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		t.Fatalf("decode ResourceClaims: %v", err)
	}
	var matches []string
	for _, claim := range claims.Items {
		if claim.Metadata.Annotations["resource.kubernetes.io/extended-resource-claim"] != "true" {
			continue
		}
		for _, owner := range claim.Metadata.OwnerReferences {
			if owner.UID == podUID {
				matches = append(matches, claim.Metadata.Name)
			}
		}
	}
	if len(matches) != 1 {
		t.Fatalf("scheduler-generated ResourceClaims for Pod %s = %v, want one", pod, matches)
	}
	return matches[0]
}

func TestTwoVPPInstancesReceiveExclusiveDPDKDevices(t *testing.T) {
	if *dpdkNode == "" {
		t.Skip("-dpdk-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "statefulset", vppDeployment, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "service", vppDeployment, "--ignore-not-found")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", vppClaimTemplate, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}
	if output, err := kubectl([]byte(vppPairManifest(*dpdkNode)), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply VPP pair: %v\n%s", err, output)
	}
	mustKubectl(t, "-n", *namespace, "rollout", "status", "statefulset/"+vppDeployment, "--timeout="+*waitTimeout)

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

	for _, test := range []struct {
		from string
		to   string
	}{
		{from: vppDeployment + "-0", to: "192.168.88.21"},
		{from: vppDeployment + "-1", to: "192.168.88.20"},
	} {
		_, _ = kubectl(nil, "-n", *namespace, "exec", test.from, "--", "vppctl", "-s", "/run/vpp/cli.sock", "ping", test.to, "repeat", "1")
		output := mustKubectl(t, "-n", *namespace, "exec", test.from, "--", "vppctl", "-s", "/run/vpp/cli.sock", "ping", test.to, "repeat", "3")
		assertOutputContains(t, output, "3 sent, 3 received", "0% packet loss")
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
apiVersion: v1
kind: Service
metadata:
  name: %[2]s
spec:
  clusterIP: None
  selector:
    app: %[2]s
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %[2]s
spec:
  serviceName: %[2]s
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
              vpp -c /tmp/vpp-startup.conf &
              VPP_PID=$!
              trap 'kill ${VPP_PID} 2>/dev/null || true' TERM INT
              for attempt in $(seq 1 30); do
                if vppctl -s /run/vpp/cli.sock show version >/dev/null 2>&1; then
                  break
                fi
                sleep 1
              done
              INTERFACE=$(vppctl -s /run/vpp/cli.sock show interface | awk '/VirtualFunctionEthernet/ { print $1; exit }')
              case "${POD_NAME}" in
                *-0) ADDRESS=192.168.88.20/24 ;;
                *-1) ADDRESS=192.168.88.21/24 ;;
                *) echo "Unexpected StatefulSet Pod name ${POD_NAME}" >&2; exit 1 ;;
              esac
              vppctl -s /run/vpp/cli.sock set interface state "${INTERFACE}" up
              vppctl -s /run/vpp/cli.sock set interface ip address "${INTERFACE}" "${ADDRESS}"
              wait "${VPP_PID}"
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          securityContext:
            capabilities:
              add: ["IPC_LOCK", "SYS_ADMIN", "SYS_RAWIO"]
          readinessProbe:
            exec:
              command:
                - /bin/sh
                - -ec
                - vppctl -s /run/vpp/cli.sock show interface address | grep -q '192.168.88.'
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

func vppMultiDeviceManifest(node string) string {
	return fmt.Sprintf(`apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: %[1]s
spec:
  selectors:
    - cel:
        expression: >-
          device.driver == 'linux-net.dra.infinitydon.com' &&
          device.attributes['linux-net.dra.infinitydon.com'].dpdk == true &&
          device.attributes['linux-net.dra.infinitydon.com'].pciVendorID == '8086' &&
          device.attributes['linux-net.dra.infinitydon.com'].pciDeviceID == '154c'
---
apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[3]s
  restartPolicy: Always
  containers:
    - name: vpp
      image: %[4]s
      command: ["/bin/sh", "-ec"]
      args:
        - |
          cat >/tmp/vpp-startup.conf <<'EOF'
          unix {
            nodaemon
            nobanner
            cli-listen /run/vpp/cli.sock
          }
          api-segment { gid 0 }
          cpu {
            relative
            main-core 0
            workers 2
          }
          dpdk {
          EOF
          env | sort | while IFS='=' read -r name value; do
            case "${name}" in
              LINUX_NET_DRA_PCI_ADDRESS_PCI_*) echo "  dev ${value}" >>/tmp/vpp-startup.conf ;;
            esac
          done
          cat >>/tmp/vpp-startup.conf <<'EOF'
            uio-driver vfio-pci
            no-multi-seg
          }
          EOF
          test "$(grep -c '^  dev ' /tmp/vpp-startup.conf)" -eq 2
          exec vpp -c /tmp/vpp-startup.conf
      securityContext:
        capabilities:
          add: ["IPC_LOCK", "SYS_ADMIN", "SYS_RAWIO"]
      readinessProbe:
        exec:
          command:
            - /bin/sh
            - -ec
            - test "$(vppctl -s /run/vpp/cli.sock show interface | grep -c '^VirtualFunctionEthernet')" -eq 2
        initialDelaySeconds: 5
        periodSeconds: 5
      resources:
        requests:
          cpu: "3"
          memory: 768Mi
          hugepages-1Gi: 2Gi
          deviceclass.resource.kubernetes.io/%[1]s: "2"
        limits:
          cpu: "3"
          memory: 768Mi
          hugepages-1Gi: 2Gi
          deviceclass.resource.kubernetes.io/%[1]s: "2"
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
`, vppMultiClass, vppMultiPod, node, vppImage)
}
