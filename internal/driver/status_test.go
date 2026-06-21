package driver

import (
	"context"
	"encoding/json"
	"strings"
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
		Claim:            types.NamespacedName{Namespace: "default", Name: "claim-a"},
		ClaimUID:         claim.UID,
		DriverName:       driver.driverName,
		PoolName:         "worker-a",
		DeviceName:       "enp8s20",
		ParentName:       "enp8s20",
		AllocationPolicy: "shared",
		Identity:         InterfaceIdentity{KernelDriver: "virtio_net", BusType: "virtio"},
		LifecycleState:   "Attached",
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
	if string(status.Data.Raw) == "" || !strings.Contains(string(status.Data.Raw), `"kernelDriver":"virtio_net"`) || !strings.Contains(string(status.Data.Raw), `"allocationPolicy":"shared"`) {
		t.Fatalf("device identity missing from status data: %s", status.Data.Raw)
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

func TestPodNetworkStatusAnnotationIsDurableAndMerged(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "pod-a", UID: types.UID("pod-uid"),
		Annotations: map[string]string{"unrelated.example/key": "keep"},
	}}
	client := kubernetesfake.NewSimpleClientset(pod)
	driver := &Driver{client: client}
	for _, cfg := range []DeviceConfig{
		{
			Claim: types.NamespacedName{Namespace: "default", Name: "claim-b"}, ParentName: "enp9s0",
			Network:         NetworkConfig{Type: "ipvlan", Mode: "l2", InterfaceName: "net2", IPPool: "lan-89", Addresses: []string{"192.168.89.11/24"}},
			HardwareAddress: "02:00:00:00:00:02",
		},
		{
			Claim: types.NamespacedName{Namespace: "default", Name: "claim-a"}, ParentName: "enp8s20",
			Network:         NetworkConfig{Type: "macvlan", Mode: "bridge", InterfaceName: "net1", IPPool: "lan-88", Gateway: "192.168.88.1", Addresses: []string{"192.168.88.11/24"}},
			HardwareAddress: "02:00:00:00:00:01",
		},
	} {
		if err := driver.setPodNetworkStatus(context.Background(), "default", "pod-a", pod.UID, cfg); err != nil {
			t.Fatal(err)
		}
	}
	updated, err := client.CoreV1().Pods("default").Get(context.Background(), "pod-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Annotations["unrelated.example/key"] != "keep" {
		t.Fatal("unrelated annotation was not preserved")
	}
	var statuses []podNetworkStatus
	if err := json.Unmarshal([]byte(updated.Annotations[NetworkStatusAnnotation]), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].InterfaceName != "net1" || statuses[1].InterfaceName != "net2" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	if statuses[0].IPs[0] != "192.168.88.11/24" || statuses[0].IPPool != "lan-88" || statuses[0].State != "Attached" {
		t.Fatalf("unexpected net1 status: %+v", statuses[0])
	}
	if raw := updated.Annotations[NetworkStatusAnnotation]; !strings.Contains(raw, "\n") || !strings.Contains(raw, `    "interfaceName": "net1"`) {
		t.Fatalf("network status is not pretty-printed: %q", raw)
	}
}

func TestPodNetworkStatusKeepsMultipleDPDKDevicesFromOneClaim(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "pod-dpdk", UID: types.UID("pod-uid"),
	}}
	client := kubernetesfake.NewSimpleClientset(pod)
	driver := &Driver{client: client}
	for _, address := range []string{"0000:02:00.0", "0000:03:00.0"} {
		cfg := DeviceConfig{
			Claim:      types.NamespacedName{Namespace: "default", Name: "claim-dpdk"},
			ParentName: address,
			Identity:   InterfaceIdentity{PCIAddress: address, KernelDriver: "vfio-pci"},
			Network:    NetworkConfig{Type: "dpdk"},
			DPDK:       &DPDKDeviceState{PCIAddress: address, CurrentDriver: "vfio-pci"},
		}
		if err := driver.setPodNetworkStatus(context.Background(), pod.Namespace, pod.Name, pod.UID, cfg); err != nil {
			t.Fatal(err)
		}
	}
	updated, err := client.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var statuses []podNetworkStatus
	if err := json.Unmarshal([]byte(updated.Annotations[NetworkStatusAnnotation]), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].PCIAddress != "0000:02:00.0" || statuses[1].PCIAddress != "0000:03:00.0" {
		t.Fatalf("multi-device DPDK statuses = %+v", statuses)
	}
}
