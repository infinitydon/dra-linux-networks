package ipamapi

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestReserveIsAtomicAndIdempotent(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		IPAllocationGVR: "IPAllocationList",
	})
	ctx := context.Background()
	first := Allocation{
		Pool: "lan-88", Address: "192.168.88.11/24", AllocationType: "Dynamic",
		Claim:    ObjectReference{Namespace: "default", Name: "claim-a", UID: types.UID("claim-a-uid")},
		Pod:      ObjectReference{Namespace: "default", Name: "pod-a", UID: types.UID("pod-a-uid")},
		NodeName: "worker-a",
	}
	if _, err := Reserve(ctx, client, first); err != nil {
		t.Fatalf("reserve first allocation: %v", err)
	}
	if _, err := Reserve(ctx, client, first); err != nil {
		t.Fatalf("repeat reservation must be idempotent: %v", err)
	}
	second := first
	second.Claim = ObjectReference{Namespace: "default", Name: "claim-b", UID: types.UID("claim-b-uid")}
	second.Pod = ObjectReference{Namespace: "default", Name: "pod-b", UID: types.UID("pod-b-uid")}
	second.NodeName = "worker-b"
	if _, err := Reserve(ctx, client, second); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("expected AlreadyExists for competing claim, got %v", err)
	}
	allocation, found, err := FindForClaim(ctx, client, first.Claim.UID)
	if err != nil || !found || allocation.NodeName != "worker-a" {
		t.Fatalf("find first allocation: found=%v allocation=%+v err=%v", found, allocation, err)
	}
	if err := DeleteForClaim(ctx, client, first.Claim.UID); err != nil {
		t.Fatalf("delete allocation: %v", err)
	}
	if _, found, err := FindForClaim(ctx, client, first.Claim.UID); err != nil || found {
		t.Fatalf("allocation still exists after delete: found=%v err=%v", found, err)
	}
}

func TestAllocationNameIsStable(t *testing.T) {
	first := AllocationName("lan-88", "192.168.88.11/24")
	second := AllocationName("lan-88", "192.168.88.11/24")
	if first != second || len(first) > 63 {
		t.Fatalf("invalid deterministic name: %q %q", first, second)
	}
}
