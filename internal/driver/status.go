package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
)

const (
	NetworkReadyCondition   corev1.PodConditionType = AttrPrefix + "/NetworkReady"
	NetworkStatusAnnotation                         = AttrPrefix + "/network-status"
)

type podNetworkStatus struct {
	InterfaceName      string   `json:"interfaceName"`
	IPs                []string `json:"ips,omitempty"`
	HardwareAddress    string   `json:"hardwareAddress,omitempty"`
	IPPool             string   `json:"ipPool,omitempty"`
	Gateway            string   `json:"gateway,omitempty"`
	ParentInterface    string   `json:"parentInterface"`
	Type               string   `json:"type"`
	Mode               string   `json:"mode,omitempty"`
	ClaimNamespace     string   `json:"claimNamespace"`
	ClaimName          string   `json:"claimName"`
	State              string   `json:"state"`
	KernelDriver       string   `json:"kernelDriver,omitempty"`
	BusType            string   `json:"busType,omitempty"`
	PCIAddress         string   `json:"pciAddress,omitempty"`
	PCIeRoot           string   `json:"pcieRoot,omitempty"`
	PCIVendorID        string   `json:"pciVendorID,omitempty"`
	PCIDeviceID        string   `json:"pciDeviceID,omitempty"`
	AllocationPolicy   string   `json:"allocationPolicy,omitempty"`
	OriginalAdminState string   `json:"originalAdminState,omitempty"`
	OriginalOperState  string   `json:"originalOperState,omitempty"`
	VendorName         string   `json:"vendorName,omitempty"`
	DeviceName         string   `json:"deviceName,omitempty"`
	SubsystemVendorID  string   `json:"subsystemVendorID,omitempty"`
	SubsystemDeviceID  string   `json:"subsystemDeviceID,omitempty"`
	CompatibleDrivers  []string `json:"compatibleKernelDrivers,omitempty"`
	IOMMUGroup         string   `json:"iommuGroup,omitempty"`
	IOMMUMode          string   `json:"iommuMode,omitempty"`
	NUMANode           int      `json:"numaNode,omitempty"`
	DeviceNodes        []string `json:"deviceNodes,omitempty"`
	CDIDeviceID        string   `json:"cdiDeviceID,omitempty"`
}

func (d *Driver) setResourceClaimDeviceStatus(ctx context.Context, cfg DeviceConfig, ready bool, reason, message, hardwareAddress string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		claim, err := d.client.ResourceV1().ResourceClaims(cfg.Claim.Namespace).Get(ctx, cfg.Claim.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if claim.UID != cfg.ClaimUID {
			return fmt.Errorf("ResourceClaim %s/%s UID changed", cfg.Claim.Namespace, cfg.Claim.Name)
		}
		status, err := allocatedDeviceStatus(claim, cfg, ready, reason, message, hardwareAddress)
		if err != nil {
			return err
		}
		replaced := false
		for i := range claim.Status.Devices {
			if sameDeviceStatusKey(claim.Status.Devices[i], status) {
				claim.Status.Devices[i] = status
				replaced = true
				break
			}
		}
		if !replaced {
			claim.Status.Devices = append(claim.Status.Devices, status)
		}
		_, err = d.client.ResourceV1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		return err
	})
}

func allocatedDeviceStatus(claim *resourceapi.ResourceClaim, cfg DeviceConfig, ready bool, reason, message, hardwareAddress string) (resourceapi.AllocatedDeviceStatus, error) {
	dataValues := map[string]interface{}{
		"ipPool":            cfg.Network.IPPool,
		"type":              cfg.Network.Type,
		"mode":              cfg.Network.Mode,
		"parentInterface":   cfg.ParentName,
		"gateway":           cfg.Network.Gateway,
		"routes":            cfg.Network.Routes,
		"kernelDriver":      cfg.Identity.KernelDriver,
		"busType":           cfg.Identity.BusType,
		"pciAddress":        cfg.Identity.PCIAddress,
		"pcieRoot":          cfg.Identity.PCIeRoot,
		"pciVendorID":       cfg.Identity.PCIVendorID,
		"pciDeviceID":       cfg.Identity.PCIDeviceID,
		"allocationPolicy":  cfg.AllocationPolicy,
		"hostInterfaceName": cfg.ParentName,
		"lifecycleState":    networkLifecycleState(cfg),
	}
	if cfg.HostDevice != nil {
		dataValues["originalAdminState"] = originalAdminState(cfg)
		dataValues["originalOperState"] = cfg.HostDevice.OriginalOperState
		dataValues["originalMTU"] = cfg.HostDevice.OriginalMTU
	}
	if cfg.DPDK != nil {
		dataValues["pciClass"] = cfg.DPDK.PCIClass
		dataValues["vendorName"] = cfg.DPDK.VendorName
		dataValues["deviceName"] = cfg.DPDK.DeviceName
		dataValues["subsystemVendorID"] = cfg.DPDK.SubsystemVendorID
		dataValues["subsystemDeviceID"] = cfg.DPDK.SubsystemDeviceID
		dataValues["compatibleKernelDrivers"] = cfg.DPDK.CompatibleKernelDrivers
		dataValues["iommuGroup"] = cfg.DPDK.IOMMUGroup
		dataValues["iommuMode"] = cfg.DPDK.IOMMUMode
		dataValues["numaNode"] = cfg.DPDK.NUMANode
		dataValues["deviceNodes"] = cfg.DPDK.DeviceNodes
		dataValues["cdiDeviceID"] = cfg.DPDK.CDIDeviceID
	}
	data, err := json.Marshal(dataValues)
	if err != nil {
		return resourceapi.AllocatedDeviceStatus{}, err
	}
	conditionStatus := metav1.ConditionFalse
	if ready {
		conditionStatus = metav1.ConditionTrue
	}
	conditions := []metav1.Condition{}
	for _, existing := range claim.Status.Devices {
		if existing.Driver == cfg.DriverName && existing.Pool == cfg.PoolName && existing.Device == cfg.DeviceName && shareIDsEqual(existing.ShareID, cfg.ShareID) {
			conditions = slices.Clone(existing.Conditions)
			break
		}
	}
	apiMeta.SetStatusCondition(&conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus,
		ObservedGeneration: claim.Generation,
		Reason:             reason,
		Message:            message,
	})
	status := resourceapi.AllocatedDeviceStatus{
		Driver:     cfg.DriverName,
		Pool:       cfg.PoolName,
		Device:     cfg.DeviceName,
		ShareID:    cfg.ShareID,
		Conditions: conditions,
		Data:       &runtime.RawExtension{Raw: data},
	}
	if cfg.Network.Type != "dpdk" {
		status.NetworkData = &resourceapi.NetworkDeviceData{
			InterfaceName:   cfg.Network.InterfaceName,
			IPs:             reportedAddresses(cfg),
			HardwareAddress: hardwareAddress,
		}
	}
	return status, nil
}

func (d *Driver) removeResourceClaimDeviceStatus(ctx context.Context, claim types.NamespacedName) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, err := d.client.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		devices := current.Status.Devices[:0]
		for _, status := range current.Status.Devices {
			if status.Driver != d.driverName {
				devices = append(devices, status)
			}
		}
		if len(devices) == len(current.Status.Devices) {
			return nil
		}
		current.Status.Devices = devices
		_, err = d.client.ResourceV1().ResourceClaims(current.Namespace).UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

func sameDeviceStatusKey(left, right resourceapi.AllocatedDeviceStatus) bool {
	return left.Driver == right.Driver && left.Pool == right.Pool && left.Device == right.Device && shareIDsEqual(left.ShareID, right.ShareID)
}

func shareIDsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func podUsesNetworkReadinessGate(pod *corev1.Pod) bool {
	for _, gate := range pod.Spec.ReadinessGates {
		if gate.ConditionType == NetworkReadyCondition {
			return true
		}
	}
	return false
}

func (d *Driver) setPodNetworkCondition(ctx context.Context, namespace, name string, uid types.UID, status corev1.ConditionStatus, reason, message string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod, err := d.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID != uid {
			return fmt.Errorf("Pod %s/%s UID changed", namespace, name)
		}
		if !podUsesNetworkReadinessGate(pod) {
			return nil
		}
		setPodCondition(&pod.Status.Conditions, corev1.PodCondition{
			Type:               NetworkReadyCondition,
			Status:             status,
			LastProbeTime:      metav1.Now(),
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		})
		_, err = d.client.CoreV1().Pods(namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{})
		return err
	})
}

func setPodCondition(conditions *[]corev1.PodCondition, condition corev1.PodCondition) {
	for i := range *conditions {
		if (*conditions)[i].Type != condition.Type {
			continue
		}
		if (*conditions)[i].Status == condition.Status {
			condition.LastTransitionTime = (*conditions)[i].LastTransitionTime
		}
		(*conditions)[i] = condition
		return
	}
	*conditions = append(*conditions, condition)
}

func (d *Driver) setPodNetworkStatus(ctx context.Context, namespace, name string, uid types.UID, cfg DeviceConfig) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod, err := d.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID != uid {
			return fmt.Errorf("Pod %s/%s UID changed", namespace, name)
		}
		statuses := []podNetworkStatus{}
		if raw := pod.Annotations[NetworkStatusAnnotation]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &statuses); err != nil {
				return fmt.Errorf("parse Pod network status annotation: %w", err)
			}
		}
		status := podNetworkStatus{
			InterfaceName:    cfg.Network.InterfaceName,
			IPs:              reportedAddresses(cfg),
			HardwareAddress:  cfg.HardwareAddress,
			IPPool:           cfg.Network.IPPool,
			Gateway:          cfg.Network.Gateway,
			ParentInterface:  cfg.ParentName,
			Type:             cfg.Network.Type,
			Mode:             cfg.Network.Mode,
			ClaimNamespace:   cfg.Claim.Namespace,
			ClaimName:        cfg.Claim.Name,
			State:            networkLifecycleState(cfg),
			KernelDriver:     cfg.Identity.KernelDriver,
			BusType:          cfg.Identity.BusType,
			PCIAddress:       cfg.Identity.PCIAddress,
			PCIeRoot:         cfg.Identity.PCIeRoot,
			PCIVendorID:      cfg.Identity.PCIVendorID,
			PCIDeviceID:      cfg.Identity.PCIDeviceID,
			AllocationPolicy: cfg.AllocationPolicy,
		}
		if cfg.HostDevice != nil {
			status.OriginalAdminState = "down"
			if cfg.HostDevice.OriginalAdminUp {
				status.OriginalAdminState = "up"
			}
			status.OriginalOperState = cfg.HostDevice.OriginalOperState
		}
		if cfg.DPDK != nil {
			status.VendorName = cfg.DPDK.VendorName
			status.DeviceName = cfg.DPDK.DeviceName
			status.SubsystemVendorID = cfg.DPDK.SubsystemVendorID
			status.SubsystemDeviceID = cfg.DPDK.SubsystemDeviceID
			status.CompatibleDrivers = slices.Clone(cfg.DPDK.CompatibleKernelDrivers)
			status.IOMMUGroup = cfg.DPDK.IOMMUGroup
			status.IOMMUMode = cfg.DPDK.IOMMUMode
			status.NUMANode = cfg.DPDK.NUMANode
			status.DeviceNodes = slices.Clone(cfg.DPDK.DeviceNodes)
			status.CDIDeviceID = cfg.DPDK.CDIDeviceID
		}
		replaced := false
		for i := range statuses {
			if statuses[i].ClaimNamespace == status.ClaimNamespace &&
				statuses[i].ClaimName == status.ClaimName &&
				statuses[i].InterfaceName == status.InterfaceName &&
				(status.PCIAddress == "" || statuses[i].PCIAddress == status.PCIAddress) {
				statuses[i] = status
				replaced = true
				break
			}
		}
		if !replaced {
			statuses = append(statuses, status)
		}
		sort.Slice(statuses, func(i, j int) bool {
			if statuses[i].InterfaceName == statuses[j].InterfaceName {
				if statuses[i].ClaimName == statuses[j].ClaimName {
					return statuses[i].PCIAddress < statuses[j].PCIAddress
				}
				return statuses[i].ClaimName < statuses[j].ClaimName
			}
			return statuses[i].InterfaceName < statuses[j].InterfaceName
		})
		raw, err := json.MarshalIndent(statuses, "", "    ")
		if err != nil {
			return err
		}
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[NetworkStatusAnnotation] = string(raw)
		_, err = d.client.CoreV1().Pods(namespace).Update(ctx, pod, metav1.UpdateOptions{})
		return err
	})
}

func (d *Driver) emitPodEvent(ctx context.Context, pod *corev1.Pod, eventType, reason, action, note string, claim *types.NamespacedName) error {
	now := metav1.NowMicro()
	event := &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pod.Name + "-",
			Namespace:    pod.Namespace,
		},
		EventTime:           now,
		ReportingController: d.driverName,
		ReportingInstance:   d.nodeName,
		Action:              action,
		Reason:              reason,
		Regarding: corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  pod.Namespace,
			Name:       pod.Name,
			UID:        pod.UID,
		},
		Note: note,
		Type: eventType,
	}
	if claim != nil {
		event.Related = &corev1.ObjectReference{
			APIVersion: "resource.k8s.io/v1",
			Kind:       "ResourceClaim",
			Namespace:  claim.Namespace,
			Name:       claim.Name,
		}
	}
	_, err := d.client.EventsV1().Events(pod.Namespace).Create(ctx, event, metav1.CreateOptions{})
	return err
}

func networkStatusMessage(cfg DeviceConfig) string {
	parts := []string{fmt.Sprintf("%s attached as %s", cfg.Network.Type, cfg.Network.InterfaceName)}
	if addresses := reportedAddresses(cfg); len(addresses) > 0 {
		parts = append(parts, "address "+strings.Join(addresses, ","))
	}
	if cfg.Network.IPPool != "" {
		parts = append(parts, "IPPool "+cfg.Network.IPPool)
	}
	parts = append(parts, "parent "+cfg.ParentName)
	return strings.Join(parts, ", ")
}

func reportedAddresses(cfg DeviceConfig) []string {
	if len(cfg.Network.Addresses) > 0 {
		return slices.Clone(cfg.Network.Addresses)
	}
	if cfg.HostDevice != nil {
		return slices.Clone(cfg.HostDevice.OriginalAddresses)
	}
	return nil
}

func networkLifecycleState(cfg DeviceConfig) string {
	if cfg.LifecycleState != "" {
		return cfg.LifecycleState
	}
	return "Attached"
}

func networkRestoredMessage(cfg DeviceConfig) string {
	return fmt.Sprintf("host device restored as %s, driver %s, PCI %s, admin state %s", cfg.ParentName, cfg.Identity.KernelDriver, cfg.Identity.PCIAddress, originalAdminState(cfg))
}

func originalAdminState(cfg DeviceConfig) string {
	if cfg.HostDevice != nil && cfg.HostDevice.OriginalAdminUp {
		return "up"
	}
	return "down"
}

func networkPreparedMessage(cfg DeviceConfig) string {
	parts := []string{fmt.Sprintf("%s prepared for interface %s", cfg.Network.Type, cfg.Network.InterfaceName)}
	if len(cfg.Network.Addresses) > 0 {
		parts = append(parts, "reserved address "+strings.Join(cfg.Network.Addresses, ","))
	}
	if cfg.Network.IPPool != "" {
		parts = append(parts, "IPPool "+cfg.Network.IPPool)
	}
	parts = append(parts, "waiting for Pod network attachment")
	return strings.Join(parts, ", ")
}
