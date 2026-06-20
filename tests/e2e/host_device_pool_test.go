//go:build e2e

package e2e

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"
)

var hostDeviceNode = flag.String("host-device-node", "", "node with the exclusive host-device NIC pool")

const (
	hostClaimTemplate = "linux-net-e2e-host-device-pool"
	hostPodA          = "linux-net-e2e-host-device-a"
	hostPodB          = "linux-net-e2e-host-device-b"
	hostPodC          = "linux-net-e2e-host-device-c"
)

type reportedNetwork struct {
	InterfaceName      string `json:"interfaceName"`
	ParentInterface    string `json:"parentInterface"`
	KernelDriver       string `json:"kernelDriver"`
	PCIAddress         string `json:"pciAddress"`
	AllocationPolicy   string `json:"allocationPolicy"`
	OriginalAdminState string `json:"originalAdminState"`
	OriginalOperState  string `json:"originalOperState"`
	State              string `json:"state"`
}

func TestExclusiveHostDevicePoolLifecycle(t *testing.T) {
	if *hostDeviceNode == "" {
		t.Skip("-host-device-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "pod", hostPodA, hostPodB, hostPodC, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", hostClaimTemplate, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}

	manifest := hostDeviceManifest(*namespace, *hostDeviceNode, hostPodA, hostPodB)
	if output, err := kubectl([]byte(manifest), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply host-device pool test: %v\n%s", err, output)
	}
	waitReady(t, hostPodA, hostPodB)

	statusA := podReportedNetwork(t, hostPodA)
	statusB := podReportedNetwork(t, hostPodB)
	if statusA.ParentInterface == statusB.ParentInterface {
		t.Fatalf("exclusive Pods received the same NIC %s", statusA.ParentInterface)
	}
	for pod, status := range map[string]reportedNetwork{hostPodA: statusA, hostPodB: statusB} {
		if status.KernelDriver == "" || status.PCIAddress == "" || status.AllocationPolicy != "exclusive" || status.State != "Attached" {
			t.Fatalf("Pod %s has incomplete host-device status: %+v", pod, status)
		}
		inside := mustKubectl(t, "-n", *namespace, "exec", pod, "--", "ip", "-o", "link", "show", "dev", status.InterfaceName)
		assertOutputContains(t, inside, status.InterfaceName)
	}

	if output, err := kubectl([]byte(hostDevicePod(*namespace, *hostDeviceNode, hostPodC)), "apply", "-f", "-"); err != nil {
		t.Fatalf("create third host-device Pod: %v\n%s", err, output)
	}
	time.Sleep(10 * time.Second)
	phase := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", hostPodC, "-o", "jsonpath={.status.phase}"))
	if phase != "Pending" {
		t.Fatalf("third Pod phase = %s, want Pending while both exclusive NICs are allocated", phase)
	}
	mustKubectl(t, "-n", *namespace, "delete", "pod", hostPodC, "--wait=true")

	mustKubectl(t, "-n", *namespace, "delete", "pod", hostPodA, "--wait=true")
	waitForHostInterface(t, statusA.ParentInterface, statusA.OriginalAdminState)
	if output, err := kubectl([]byte(hostDevicePod(*namespace, *hostDeviceNode, hostPodC)), "apply", "-f", "-"); err != nil {
		t.Fatalf("recreate third host-device Pod: %v\n%s", err, output)
	}
	waitReady(t, hostPodC)
	statusC := podReportedNetwork(t, hostPodC)
	if statusC.ParentInterface != statusA.ParentInterface {
		t.Fatalf("replacement Pod received %s, want released NIC %s", statusC.ParentInterface, statusA.ParentInterface)
	}
}

func waitReady(t *testing.T, pods ...string) {
	t.Helper()
	args := []string{"-n", *namespace, "wait", "--for=condition=Ready"}
	for _, pod := range pods {
		args = append(args, "pod/"+pod)
	}
	args = append(args, "--timeout="+*waitTimeout)
	mustKubectl(t, args...)
}

func podReportedNetwork(t *testing.T, pod string) reportedNetwork {
	t.Helper()
	raw := mustKubectl(t, "-n", *namespace, "get", "pod", pod, "-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
	var statuses []reportedNetwork
	if err := json.Unmarshal([]byte(raw), &statuses); err != nil || len(statuses) != 1 {
		t.Fatalf("decode Pod %s network status: %v, value %s", pod, err, raw)
	}
	return statuses[0]
}

func waitForHostInterface(t *testing.T, interfaceName, adminState string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		plugin := nodePluginPod(t)
		output, err := kubectl(nil, "-n", "kube-system", "exec", plugin, "--", "ip", "-o", "link", "show", "dev", interfaceName)
		if err == nil && strings.Contains(output, interfaceName) {
			if adminState == "up" && !strings.Contains(output, "UP") {
				t.Fatalf("restored interface %s is not administratively UP: %s", interfaceName, output)
			}
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("interface %s did not return to the host", interfaceName)
}

func nodePluginPod(t *testing.T) string {
	t.Helper()
	rows := mustKubectl(t, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=linux-net-dra", "-o", "custom-columns=NAME:.metadata.name,NODE:.spec.nodeName", "--no-headers")
	for _, row := range strings.Split(rows, "\n") {
		fields := strings.Fields(row)
		if len(fields) == 2 && fields[1] == *hostDeviceNode && !strings.Contains(fields[0], "controller") {
			return fields[0]
		}
	}
	t.Fatalf("node plugin Pod not found on %s", *hostDeviceNode)
	return ""
}

func hostDeviceManifest(namespace, node, podA, podB string) string {
	return fmt.Sprintf(`apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  spec:
    devices:
      requests:
        - name: nic
          exactly:
            deviceClassName: linux-net-host-device
            selectors:
              - cel:
                  expression: 'device.attributes["linux-net.dra.infinitydon.com"].kernelDriver == "e1000"'
    config:
      - requests: ["nic"]
        opaque:
          driver: linux-net.dra.infinitydon.com
          parameters:
            type: host-device
---
%[3]s
---
%[4]s
`, hostClaimTemplate, namespace, hostDevicePod(namespace, node, podA), hostDevicePod(namespace, node, podB))
}

func hostDevicePod(namespace, node, name string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  nodeSelector:
    kubernetes.io/hostname: %s
  restartPolicy: Never
  readinessGates:
    - conditionType: linux-net.dra.infinitydon.com/NetworkReady
  resourceClaims:
    - name: nic
      resourceClaimTemplateName: %s
  containers:
    - name: network-test
      image: nicolaka/netshoot:v0.15@sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61
      command: ["sleep", "3600"]
      resources:
        claims:
          - name: nic
`, name, namespace, node, hostClaimTemplate)
}
