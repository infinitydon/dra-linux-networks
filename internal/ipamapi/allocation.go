package ipamapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var (
	IPPoolGVR = schema.GroupVersionResource{
		Group: "linux-net.dra.infinitydon.com", Version: "v1alpha1", Resource: "ippools",
	}
	IPAllocationGVR = schema.GroupVersionResource{
		Group: "linux-net.dra.infinitydon.com", Version: "v1alpha1", Resource: "ipallocations",
	}
)

type ObjectReference struct {
	Namespace string    `json:"namespace,omitempty"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

type Allocation struct {
	Name           string
	Pool           string
	Address        string
	AllocationType string
	Claim          ObjectReference
	Pod            ObjectReference
	NodeName       string
}

func AllocationName(pool, address string) string {
	sum := sha256.Sum256([]byte(pool + "\x00" + address))
	return "ipa-" + hex.EncodeToString(sum[:16])
}

func NewAllocationObject(allocation Allocation) *unstructured.Unstructured {
	name := AllocationName(allocation.Pool, allocation.Address)
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "linux-net.dra.infinitydon.com/v1alpha1",
		"kind":       "IPAllocation",
		"metadata": map[string]interface{}{
			"name": name,
			"labels": map[string]interface{}{
				"linux-net.dra.infinitydon.com/pool": allocation.Pool,
			},
		},
		"spec": map[string]interface{}{
			"poolRef":        allocation.Pool,
			"address":        allocation.Address,
			"allocationType": allocation.AllocationType,
			"claimRef": map[string]interface{}{
				"namespace": allocation.Claim.Namespace,
				"name":      allocation.Claim.Name,
				"uid":       string(allocation.Claim.UID),
			},
			"podRef": map[string]interface{}{
				"namespace": allocation.Pod.Namespace,
				"name":      allocation.Pod.Name,
				"uid":       string(allocation.Pod.UID),
			},
			"nodeName": allocation.NodeName,
		},
	}}
}

func Reserve(ctx context.Context, client dynamic.Interface, allocation Allocation) (*unstructured.Unstructured, error) {
	resource := client.Resource(IPAllocationGVR)
	desired := NewAllocationObject(allocation)
	created, err := resource.Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	existing, getErr := resource.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if getErr != nil {
		return nil, getErr
	}
	parsed, parseErr := ParseAllocation(existing)
	if parseErr != nil {
		return nil, parseErr
	}
	if parsed.Claim.UID == allocation.Claim.UID && parsed.Pool == allocation.Pool && parsed.Address == allocation.Address {
		return existing, nil
	}
	return nil, apierrors.NewAlreadyExists(IPAllocationGVR.GroupResource(), desired.GetName())
}

func DeleteForClaim(ctx context.Context, client dynamic.Interface, claimUID types.UID) error {
	list, err := client.Resource(IPAllocationGVR).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for i := range list.Items {
		allocation, err := ParseAllocation(&list.Items[i])
		if err != nil || allocation.Claim.UID != claimUID {
			continue
		}
		uid := list.Items[i].GetUID()
		policy := metav1.DeletePropagationBackground
		err = client.Resource(IPAllocationGVR).Delete(ctx, list.Items[i].GetName(), metav1.DeleteOptions{
			Preconditions:     &metav1.Preconditions{UID: &uid},
			PropagationPolicy: &policy,
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func FindForClaim(ctx context.Context, client dynamic.Interface, claimUID types.UID) (Allocation, bool, error) {
	list, err := client.Resource(IPAllocationGVR).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return Allocation{}, false, nil
	}
	if err != nil {
		return Allocation{}, false, err
	}
	for i := range list.Items {
		allocation, err := ParseAllocation(&list.Items[i])
		if err != nil {
			continue
		}
		if allocation.Claim.UID == claimUID {
			return allocation, true, nil
		}
	}
	return Allocation{}, false, nil
}

func ParseAllocation(obj *unstructured.Unstructured) (Allocation, error) {
	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		return Allocation{}, fmt.Errorf("IPAllocation %s has no spec", obj.GetName())
	}
	allocation := Allocation{Name: obj.GetName()}
	allocation.Pool, _, _ = unstructured.NestedString(spec, "poolRef")
	allocation.Address, _, _ = unstructured.NestedString(spec, "address")
	allocation.AllocationType, _, _ = unstructured.NestedString(spec, "allocationType")
	allocation.NodeName, _, _ = unstructured.NestedString(spec, "nodeName")
	allocation.Claim = referenceFromSpec(spec, "claimRef")
	allocation.Pod = referenceFromSpec(spec, "podRef")
	if strings.TrimSpace(allocation.Pool) == "" || strings.TrimSpace(allocation.Address) == "" || allocation.Claim.UID == "" {
		return Allocation{}, fmt.Errorf("IPAllocation %s is missing poolRef, address, or claimRef.uid", obj.GetName())
	}
	return allocation, nil
}

func referenceFromSpec(spec map[string]interface{}, field string) ObjectReference {
	ref := ObjectReference{}
	ref.Namespace, _, _ = unstructured.NestedString(spec, field, "namespace")
	ref.Name, _, _ = unstructured.NestedString(spec, field, "name")
	uid, _, _ := unstructured.NestedString(spec, field, "uid")
	ref.UID = types.UID(uid)
	return ref
}
