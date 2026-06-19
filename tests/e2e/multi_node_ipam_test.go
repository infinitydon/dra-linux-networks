//go:build e2e

package e2e

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var (
	staticNode    = flag.String("static-node", "", "worker node for the static allocation pod")
	dynamicNode   = flag.String("dynamic-node", "", "worker node for the dynamic allocation pod")
	namespace     = flag.String("namespace", "default", "test namespace")
	poolName      = flag.String("pool", "lan-88", "IPPool name")
	staticCIDR    = flag.String("static-address", "192.168.88.10/24", "reserved static address")
	gateway       = flag.String("gateway", "192.168.88.1", "gateway address to ping")
	waitTimeout   = flag.String("wait-timeout", "240s", "kubectl wait timeout")
	keepResources = flag.Bool("keep-resources", false, "leave test resources in the cluster")
)

const (
	claimTemplateName = "linux-net-e2e-multi-node-ipam"
	staticPodName     = "linux-net-e2e-static-allocation"
	dynamicPodName    = "linux-net-e2e-dynamic-allocation"
)

func TestMultiNodeIPAMConnectivity(t *testing.T) {
	if *staticNode == "" || *dynamicNode == "" {
		t.Fatal("-static-node and -dynamic-node are required")
	}
	if *staticNode == *dynamicNode {
		t.Fatal("static and dynamic pods must use different nodes")
	}
	staticIP, _, err := net.ParseCIDR(*staticCIDR)
	if err != nil {
		t.Fatalf("parse static address: %v", err)
	}

	cleanup := func() {
		if *keepResources {
			return
		}
		_, _ = kubectl(nil, "-n", *namespace, "delete", "pod", staticPodName, dynamicPodName, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", claimTemplateName, "--ignore-not-found")
	}
	cleanup()
	defer cleanup()

	manifest := multiNodeManifest(*namespace, *poolName, *staticCIDR, *staticNode, *dynamicNode)
	if output, err := kubectl([]byte(manifest), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply test resources: %v\n%s", err, output)
	}
	if output, err := kubectl(nil, "-n", *namespace, "wait", "--for=condition=Ready", "pod/"+staticPodName, "pod/"+dynamicPodName, "--timeout="+*waitTimeout); err != nil {
		t.Fatalf("wait for test pods: %v\n%s", err, output)
	}

	var dynamicIP string
	t.Run("StaticAddressAssignedOnPinnedWorker", func(t *testing.T) {
		assertPodNode(t, staticPodName, *staticNode)
		cidr := interfaceCIDR(t, staticPodName)
		if cidr != *staticCIDR {
			t.Fatalf("static net1 address = %s, want %s", cidr, *staticCIDR)
		}
	})

	t.Run("DynamicAddressAssignedOnPinnedWorker", func(t *testing.T) {
		assertPodNode(t, dynamicPodName, *dynamicNode)
		cidr := interfaceCIDR(t, dynamicPodName)
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatalf("parse dynamic net1 address %q: %v", cidr, err)
		}
		if ip.Equal(staticIP) {
			t.Fatalf("dynamic allocation reused static address %s", ip)
		}
		dynamicIP = ip.String()
	})

	t.Run("ClusterAllocationRecordsMatchWorkers", func(t *testing.T) {
		output := mustKubectl(t, "get", "lnipa", "-o", "custom-columns=ADDRESS:.spec.address,NODE:.spec.nodeName,POD:.spec.podRef.name", "--no-headers")
		assertOutputContains(t, output, *staticCIDR, *staticNode, staticPodName)
		assertOutputContains(t, output, dynamicIP, *dynamicNode, dynamicPodName)
	})

	t.Run("PodsReportSecondaryNetworkReady", func(t *testing.T) {
		for _, pod := range []string{staticPodName, dynamicPodName} {
			status := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", pod,
				"-o", `jsonpath={.status.conditions[?(@.type=="linux-net.dra.infinitydon.com/NetworkReady")].status}`))
			if status != "True" {
				t.Fatalf("pod %s NetworkReady = %q, want True", pod, status)
			}
		}
	})

	t.Run("PodsPersistAttachedNetworkStatus", func(t *testing.T) {
		for _, pod := range []string{staticPodName, dynamicPodName} {
			status := mustKubectl(t, "-n", *namespace, "get", "pod", pod,
				"-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
			assertOutputContains(t, status, `"interfaceName":"net1"`, `"ipPool":"`+*poolName+`"`, `"state":"Attached"`)
		}
	})

	t.Run("ResourceClaimsReportReadyNetworkData", func(t *testing.T) {
		for _, pod := range []string{staticPodName, dynamicPodName} {
			claim := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", pod,
				"-o", "jsonpath={.status.resourceClaimStatuses[0].resourceClaimName}"))
			if claim == "" {
				t.Fatalf("pod %s has no generated ResourceClaim name", pod)
			}
			status := mustKubectl(t, "-n", *namespace, "get", "resourceclaim", claim,
				"-o", `jsonpath={.status.devices[0].conditions[?(@.type=="Ready")].status} {.status.devices[0].networkData.interfaceName} {.status.devices[0].networkData.ips[0]} {.status.devices[0].data.ipPool}`)
			assertOutputContains(t, status, "True", "net1", *poolName)
		}
	})

	t.Run("PodDescribeReportsNetworkLifecycleEvents", func(t *testing.T) {
		for _, pod := range []string{staticPodName, dynamicPodName} {
			description := mustKubectl(t, "-n", *namespace, "describe", "pod", pod)
			assertOutputContains(t, description, "LinuxNetworkPrepared", "LinuxNetworkAttached", "IPPool "+*poolName)
		}
	})

	t.Run("GatewayReachableFromStaticAndDynamicAllocations", func(t *testing.T) {
		ping(t, staticPodName, *gateway)
		ping(t, dynamicPodName, *gateway)
	})

	t.Run("BidirectionalInterNodeSecondaryNetworkConnectivity", func(t *testing.T) {
		ping(t, staticPodName, dynamicIP)
		ping(t, dynamicPodName, staticIP.String())
	})

	t.Run("ControllerReportsStaticAndDynamicAllocations", func(t *testing.T) {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			status, err := kubectl(nil, "get", "ippools.linux-net.dra.infinitydon.com", *poolName,
				"-o", "jsonpath={.status.allocated} {.status.dynamicAllocated} {.status.staticAllocated} {.status.conditions[0].status}")
			if err == nil {
				var total, dynamic, static int
				var ready string
				if _, scanErr := fmt.Sscanf(status, "%d %d %d %s", &total, &dynamic, &static, &ready); scanErr == nil &&
					total >= 2 && dynamic >= 1 && static >= 1 && ready == "True" {
					return
				}
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatal("controller did not report at least 2 allocated, 1 dynamic, 1 static, Ready")
	})

	t.Run("AllocationRecordsReleasedAfterCleanup", func(t *testing.T) {
		if *keepResources {
			t.Skip("resource cleanup disabled")
		}
		cleanup()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			output, err := kubectl(nil, "get", "lnipa", "-o", "custom-columns=POD:.spec.podRef.name", "--no-headers")
			if err == nil && !strings.Contains(output, staticPodName) && !strings.Contains(output, dynamicPodName) {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatal("test IPAllocation records were not released")
	})
}

func assertPodNode(t *testing.T, pod, want string) {
	t.Helper()
	got := strings.TrimSpace(mustKubectl(t, "-n", *namespace, "get", "pod", pod, "-o", "jsonpath={.spec.nodeName}"))
	if got != want {
		t.Fatalf("pod %s node = %s, want %s", pod, got, want)
	}
}

func interfaceCIDR(t *testing.T, pod string) string {
	t.Helper()
	output := mustKubectl(t, "-n", *namespace, "exec", pod, "--", "ip", "-4", "-o", "addr", "show", "dev", "net1")
	fields := strings.Fields(output)
	for i, field := range fields {
		if field == "inet" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	t.Fatalf("net1 has no IPv4 address: %s", output)
	return ""
}

func ping(t *testing.T, pod, target string) {
	t.Helper()
	if output, err := kubectl(nil, "-n", *namespace, "exec", pod, "--", "ping", "-c", "4", "-W", "2", target); err != nil {
		t.Fatalf("%s cannot ping %s: %v\n%s", pod, target, err, output)
	}
}

func assertOutputContains(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(output, value) {
			t.Fatalf("output does not contain %q:\n%s", value, output)
		}
	}
}

func mustKubectl(t *testing.T, args ...string) string {
	t.Helper()
	output, err := kubectl(nil, args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func kubectl(stdin []byte, args ...string) (string, error) {
	binary := os.Getenv("KUBECTL")
	if binary == "" {
		binary = "kubectl"
	}
	command := exec.Command(binary, args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func multiNodeManifest(namespace, pool, address, staticWorker, dynamicWorker string) string {
	return fmt.Sprintf(`apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  spec:
    devices:
      requests:
        - name: net1
          exactly:
            deviceClassName: linux-net-macvlan
      config:
        - requests: ["net1"]
          opaque:
            driver: linux-net.dra.infinitydon.com
            parameters:
              type: macvlan
              mode: bridge
              interfaceName: net1
              mtu: 9000
              ipPool: %[3]s
---
apiVersion: v1
kind: Pod
metadata:
  name: %[4]s
  namespace: %[2]s
  annotations:
    linux-net.dra.infinitydon.com/net1.ip-pool: %[3]s
    linux-net.dra.infinitydon.com/net1.address: %[5]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[6]s
  restartPolicy: Never
  readinessGates:
    - conditionType: linux-net.dra.infinitydon.com/NetworkReady
  resourceClaims:
    - name: net1
      resourceClaimTemplateName: %[1]s
  containers:
    - name: network-test
      image: nicolaka/netshoot:v0.15@sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61
      command: ["sleep", "3600"]
      resources:
        claims:
          - name: net1
---
apiVersion: v1
kind: Pod
metadata:
  name: %[7]s
  namespace: %[2]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[8]s
  restartPolicy: Never
  readinessGates:
    - conditionType: linux-net.dra.infinitydon.com/NetworkReady
  resourceClaims:
    - name: net1
      resourceClaimTemplateName: %[1]s
  containers:
    - name: network-test
      image: nicolaka/netshoot:v0.15@sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61
      command: ["sleep", "3600"]
      resources:
        claims:
          - name: net1
`, claimTemplateName, namespace, pool, staticPodName, address, staticWorker, dynamicPodName, dynamicWorker)
}
