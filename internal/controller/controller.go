package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/infinitydon/dra-linux-networks/internal/ipamapi"
)

type Controller struct {
	client   kubernetes.Interface
	dynamic  dynamic.Interface
	interval time.Duration
}

func New(client kubernetes.Interface, dynamicClient dynamic.Interface, interval time.Duration) (*Controller, error) {
	if client == nil || dynamicClient == nil {
		return nil, fmt.Errorf("Kubernetes clients are required")
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Controller{client: client, dynamic: dynamicClient, interval: interval}, nil
}

func (c *Controller) Run(ctx context.Context) error {
	if err := c.Reconcile(ctx); err != nil {
		klog.ErrorS(err, "Initial IP allocation reconciliation failed")
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.Reconcile(ctx); err != nil {
				klog.ErrorS(err, "IP allocation reconciliation failed")
			}
		}
	}
}

func (c *Controller) Reconcile(ctx context.Context) error {
	list, err := c.dynamic.Resource(ipamapi.IPAllocationGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list IP allocations: %w", err)
	}
	counts := map[string]poolCounts{}
	for i := range list.Items {
		obj := &list.Items[i]
		allocation, err := ipamapi.ParseAllocation(obj)
		if err != nil {
			klog.ErrorS(err, "Ignoring malformed IPAllocation", "name", obj.GetName())
			continue
		}
		stale, err := c.allocationIsStale(ctx, allocation)
		if err != nil {
			return err
		}
		if stale {
			if err := c.deleteAllocation(ctx, obj); err != nil {
				return err
			}
			continue
		}
		count := counts[allocation.Pool]
		count.Total++
		if allocation.AllocationType == "Static" {
			count.Static++
		} else {
			count.Dynamic++
		}
		counts[allocation.Pool] = count
	}
	return c.updatePoolStatuses(ctx, counts)
}

func (c *Controller) allocationIsStale(ctx context.Context, allocation ipamapi.Allocation) (bool, error) {
	claim, err := c.client.ResourceV1().ResourceClaims(allocation.Claim.Namespace).Get(ctx, allocation.Claim.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get ResourceClaim %s/%s: %w", allocation.Claim.Namespace, allocation.Claim.Name, err)
	}
	return claim.UID != allocation.Claim.UID, nil
}

func (c *Controller) deleteAllocation(ctx context.Context, obj *unstructured.Unstructured) error {
	uid := obj.GetUID()
	err := c.dynamic.Resource(ipamapi.IPAllocationGVR).Delete(ctx, obj.GetName(), metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete stale IPAllocation %s: %w", obj.GetName(), err)
	}
	klog.InfoS("Deleted stale IPAllocation", "name", obj.GetName())
	return nil
}

type poolCounts struct {
	Total   int64
	Dynamic int64
	Static  int64
}

func (c *Controller) updatePoolStatuses(ctx context.Context, counts map[string]poolCounts) error {
	pools, err := c.dynamic.Resource(ipamapi.IPPoolGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list IP pools: %w", err)
	}
	for i := range pools.Items {
		pool := pools.Items[i].DeepCopy()
		count := counts[pool.GetName()]
		status := map[string]interface{}{
			"allocated":        count.Total,
			"dynamicAllocated": count.Dynamic,
			"staticAllocated":  count.Static,
			"conditions": []interface{}{
				map[string]interface{}{
					"type":               "Ready",
					"status":             "True",
					"observedGeneration": pool.GetGeneration(),
					"lastTransitionTime": transitionTime(pool),
					"reason":             "Reconciled",
					"message":            "IP allocations are reconciled",
				},
			},
		}
		current, _, _ := unstructured.NestedMap(pool.Object, "status")
		if reflect.DeepEqual(current, status) {
			continue
		}
		if err := unstructured.SetNestedMap(pool.Object, status, "status"); err != nil {
			return err
		}
		if _, err := c.dynamic.Resource(ipamapi.IPPoolGVR).UpdateStatus(ctx, pool, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update IPPool %s status: %w", pool.GetName(), err)
		}
	}
	return nil
}

func transitionTime(pool *unstructured.Unstructured) string {
	conditions, ok, _ := unstructured.NestedSlice(pool.Object, "status", "conditions")
	if ok {
		for _, raw := range conditions {
			condition, ok := raw.(map[string]interface{})
			if !ok || condition["type"] != "Ready" || condition["status"] != "True" {
				continue
			}
			if value, ok := condition["lastTransitionTime"].(string); ok && value != "" {
				return value
			}
		}
	}
	return time.Now().UTC().Format(time.RFC3339)
}
