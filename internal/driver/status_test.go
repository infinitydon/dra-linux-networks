package driver

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestResourceClaimDeviceStatusIncludesNetworkData(t *testing.T) {
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "claim-a", UID: types.UID("claim-uid"), Generation: 2,
	}}
	client := kubernetesfake.NewSimpleClientset(claim)
	driver := &Driver{driverName: "linux-net.dra.infinitydon.com", client: client}
	cfg := DeviceConfig{
		Claim:      types.NamespacedName{Namespace: "default", Name: "claim-a"},
		ClaimUID:   claim.UID,
		DriverName: driver.driverName,
		PoolName:   "worker-a",
		DeviceName: "enp8s20",
		ParentName: "enp8s20",
		Network: NetworkConfig{
			Type: "macvlan", Mode: "bridge", InterfaceName: "net1", IPPool: "lan-88",
			Addresses: []string{"192.168.88.10/24"},
		},
	}
	if err := driver.setResourceClaimDeviceStatus(context.Background(), cfg, true, "NetworkAttached", "attached", "02:00:00:00:00:01"); err != nil {
		t.Fatal(err)
	}
	updated, err := client.ResourceV1().ResourceClaims("default").Get(context.Background(), "claim-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Status.Devices) != 1 {
		t.Fatalf("device statuses = %d, want 1", len(updated.Status.Devices))
	}
	status := updated.Status.Devices[0]
	if status.NetworkData == nil || status.NetworkData.InterfaceName != "net1" || status.NetworkData.IPs[0] != "192.168.88.10/24" {
		t.Fatalf("unexpected network data: %+v", status.NetworkData)
	}
	if len(status.Conditions) != 1 || status.Conditions[0].Status != metav1.ConditionTrue || status.Conditions[0].Reason != "NetworkAttached" {
		t.Fatalf("unexpected conditions: %+v", status.Conditions)
	}
}

func TestPodNetworkConditionRequiresReadinessGate(t *testing.T) {
	tests := []struct {
		name      string
		withGate  bool
		wantCount int
	}{
		{name: "with readiness gate", withGate: true, wantCount: 1},
		{name: "without readiness gate", withGate: false, wantCount: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-a", UID: types.UID("pod-uid")}}
			if test.withGate {
				pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: NetworkReadyCondition}}
			}
			client := kubernetesfake.NewSimpleClientset(pod)
			driver := &Driver{client: client}
			if err := driver.setPodNetworkCondition(context.Background(), "default", "pod-a", pod.UID, corev1.ConditionTrue, "NetworkAttached", "attached"); err != nil {
				t.Fatal(err)
			}
			updated, err := client.CoreV1().Pods("default").Get(context.Background(), "pod-a", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, condition := range updated.Status.Conditions {
				if condition.Type == NetworkReadyCondition {
					count++
				}
			}
			if count != test.wantCount {
				t.Fatalf("NetworkReady conditions = %d, want %d", count, test.wantCount)
			}
		})
	}
}
