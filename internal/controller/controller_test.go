package controller

import (
	"context"
	"testing"
	"time"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/infinitydon/dra-linux-networks/internal/ipamapi"
)

func TestReconcileKeepsLiveAndDeletesStaleAllocations(t *testing.T) {
	ctx := context.Background()
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{
		Namespace: "default", Name: "live-claim", UID: types.UID("live-uid"),
	}}
	typedClient := kubernetesfake.NewSimpleClientset(claim)
	pool := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "linux-net.dra.infinitydon.com/v1alpha1",
		"kind":       "IPPool",
		"metadata":   map[string]interface{}{"name": "lan-88", "generation": int64(1)},
		"spec":       map[string]interface{}{"subnet": "192.168.88.0/24"},
	}}
	live := ipamapi.NewAllocationObject(ipamapi.Allocation{
		Pool: "lan-88", Address: "192.168.88.11/24", AllocationType: "Dynamic",
		Claim:    ipamapi.ObjectReference{Namespace: "default", Name: "live-claim", UID: types.UID("live-uid")},
		Pod:      ipamapi.ObjectReference{Namespace: "default", Name: "live-pod", UID: types.UID("live-pod-uid")},
		NodeName: "worker-a",
	})
	stale := ipamapi.NewAllocationObject(ipamapi.Allocation{
		Pool: "lan-88", Address: "192.168.88.12/24", AllocationType: "Dynamic",
		Claim:    ipamapi.ObjectReference{Namespace: "default", Name: "gone-claim", UID: types.UID("gone-uid")},
		Pod:      ipamapi.ObjectReference{Namespace: "default", Name: "gone-pod", UID: types.UID("gone-pod-uid")},
		NodeName: "worker-b",
	})
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		ipamapi.IPAllocationGVR: "IPAllocationList",
		ipamapi.IPPoolGVR:       "IPPoolList",
	}, pool, live, stale)
	reconciler, err := New(typedClient, dynamicClient, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	allocations, err := dynamicClient.Resource(ipamapi.IPAllocationGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations.Items) != 1 || allocations.Items[0].GetName() != live.GetName() {
		t.Fatalf("unexpected allocations after reconciliation: %+v", allocations.Items)
	}
	updatedPool, err := dynamicClient.Resource(ipamapi.IPPoolGVR).Get(ctx, "lan-88", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	allocated, _, _ := unstructured.NestedInt64(updatedPool.Object, "status", "allocated")
	if allocated != 1 {
		t.Fatalf("expected pool allocated count 1, got %d", allocated)
	}
}
