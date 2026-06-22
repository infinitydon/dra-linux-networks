package driver

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

type capturingHelper struct {
	resources resourceslice.DriverResources
}

func TestPodInterfaceNameIncrementsAndSupportsClaimOverride(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	driver := &Driver{store: store}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		UID: types.UID("pod-uid"),
		Annotations: map[string]string{
			AttrPrefix + "/network-b.interface-name": "storage0",
		},
	}}
	claimA := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{PodClaimNameAnnotation: "network-a"}}}
	claimB := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{PodClaimNameAnnotation: "network-b"}}}

	name, err := driver.podInterfaceName(pod, claimA, "net1", "claim-a/enp8s20", "net1")
	if err != nil || name != "net1" {
		t.Fatalf("first interface = %q, err = %v", name, err)
	}
	if err := store.SetDevice(pod.UID, "claim-a/enp8s20", DeviceConfig{Network: NetworkConfig{Type: "macvlan", InterfaceName: name}}); err != nil {
		t.Fatal(err)
	}

	name, err = driver.podInterfaceName(pod, &resourceapi.ResourceClaim{}, "net1", "claim-b/enp8s20", "net1")
	if err != nil || name != "net2" {
		t.Fatalf("incremented interface = %q, err = %v", name, err)
	}
	name, err = driver.podInterfaceName(pod, claimB, "net1", "claim-b/enp8s20", "net1")
	if err != nil || name != "storage0" {
		t.Fatalf("overridden interface = %q, err = %v", name, err)
	}
}

func (h *capturingHelper) PublishResources(_ context.Context, resources resourceslice.DriverResources) error {
	h.resources = resources
	return nil
}

func (*capturingHelper) RegistrationStatus() *registerapi.RegistrationStatus { return nil }
func (*capturingHelper) Stop()                                               {}

func TestPublishEmptyInventoryWithdrawsNodePool(t *testing.T) {
	helper := &capturingHelper{}
	driver := &Driver{
		nodeName: "worker-a",
		cfg:      &config.Config{},
		helper:   helper,
	}
	if err := driver.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(helper.resources.Pools) != 0 {
		t.Fatalf("published pools = %+v, want none", helper.resources.Pools)
	}
}

func TestPodStaticAddressRequiresMatchingPoolReference(t *testing.T) {
	base := AttrPrefix + "/net1"
	tests := []struct {
		name         string
		annotations  map[string]string
		expectedPool string
		wantAddress  string
		wantError    string
	}{
		{
			name: "matching pool",
			annotations: map[string]string{
				base + ".ip-pool": "lan-88",
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantAddress:  "192.168.88.10/24",
		},
		{
			name: "missing pool annotation",
			annotations: map[string]string{
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantError:    "must be set together",
		},
		{
			name: "mismatched pool",
			annotations: map[string]string{
				base + ".ip-pool": "other-pool",
				base + ".address": "192.168.88.10/24",
			},
			expectedPool: "lan-88",
			wantError:    "does not match",
		},
		{
			name: "claim has no pool",
			annotations: map[string]string{
				base + ".ip-pool": "lan-88",
				base + ".address": "192.168.88.10/24",
			},
			wantError: "requires ipPool",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			address, err := podStaticAddress(test.annotations, "net1", "net1", test.expectedPool)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want substring %q", err, test.wantError)
				}
				return
			}
			if err != nil || address != test.wantAddress {
				t.Fatalf("address = %q, err = %v; want %q", address, err, test.wantAddress)
			}
		})
	}
}
