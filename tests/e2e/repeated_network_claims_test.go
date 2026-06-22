//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
)

const (
	repeatedClaimTemplate = "linux-net-e2e-reusable-macvlan"
	repeatedClaimPod      = "linux-net-e2e-multiple-macvlan"
	repeatedNetshootImage = "nicolaka/netshoot:v0.15@sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61"
)

func TestRepeatedTemplateAllocatesUniquePodInterfaceNames(t *testing.T) {
	if *dynamicNode == "" {
		t.Skip("-dynamic-node is not set")
	}
	cleanup := func() {
		_, _ = kubectl(nil, "-n", *namespace, "delete", "pod", repeatedClaimPod, "--ignore-not-found", "--wait=true")
		_, _ = kubectl(nil, "-n", *namespace, "delete", "resourceclaimtemplate", repeatedClaimTemplate, "--ignore-not-found")
	}
	cleanup()
	if !*keepResources {
		defer cleanup()
	}

	manifest := repeatedNetworkClaimManifest(*dynamicNode, *poolName)
	if output, err := kubectl([]byte(manifest), "apply", "-f", "-"); err != nil {
		t.Fatalf("apply repeated network claim resources: %v\n%s", err, output)
	}
	if output, err := kubectl(nil, "-n", *namespace, "wait", "--for=condition=Ready", "pod/"+repeatedClaimPod, "--timeout="+*waitTimeout); err != nil {
		t.Fatalf("wait for repeated claim Pod: %v\n%s", err, output)
	}
	assertPodNode(t, repeatedClaimPod, *dynamicNode)

	output := mustKubectl(t, "-n", *namespace, "exec", repeatedClaimPod, "--", "sh", "-ec", `
ip -o link show net1
ip -o link show net2
test -n "$(ip -o -4 addr show net1)"
test -n "$(ip -o -4 addr show net2)"
`)
	assertOutputContains(t, output, "net1", "net2")

	status := mustKubectl(t, "-n", *namespace, "get", "pod", repeatedClaimPod,
		"-o", `jsonpath={.metadata.annotations.linux-net\.dra\.infinitydon\.com/network-status}`)
	assertOutputContains(t, status, `"interfaceName": "net1"`, `"interfaceName": "net2"`, `"parentInterface": "enp8s20"`)

	claims := strings.Fields(mustKubectl(t, "-n", *namespace, "get", "pod", repeatedClaimPod,
		"-o", `jsonpath={range .status.resourceClaimStatuses[*]}{.resourceClaimName}{" "}{end}`))
	if len(claims) != 2 {
		t.Fatalf("generated claim count = %d, want 2: %v", len(claims), claims)
	}
	for _, claim := range claims {
		allocation := mustKubectl(t, "-n", *namespace, "get", "resourceclaim", claim,
			"-o", `jsonpath={.status.allocation.devices.results[0].device} {.status.devices[0].conditions[?(@.type=="Ready")].status}`)
		assertOutputContains(t, allocation, "enp8s20", "True")
	}
}

func repeatedNetworkClaimManifest(node, pool string) string {
	return fmt.Sprintf(`apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: %[1]s
spec:
  spec:
    devices:
      requests:
        - name: net1
          exactly:
            deviceClassName: linux-net-macvlan
            selectors:
              - cel:
                  expression: device.attributes['linux-net.dra.infinitydon.com'].interface == 'enp8s20'
      config:
        - requests: ["net1"]
          opaque:
            driver: linux-net.dra.infinitydon.com
            parameters:
              type: macvlan
              mode: bridge
              interfaceName: net1
              mtu: 9000
              ipPool: %[4]s
---
apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[3]s
  restartPolicy: Never
  resourceClaims:
    - name: network-a
      resourceClaimTemplateName: %[1]s
    - name: network-b
      resourceClaimTemplateName: %[1]s
  containers:
    - name: shell
      image: %[5]s
      command: ["sleep", "3600"]
      resources:
        claims:
          - name: network-a
          - name: network-b
`, repeatedClaimTemplate, repeatedClaimPod, node, pool, repeatedNetshootImage)
}
