package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/containerd/nri/pkg/api"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

func attachHostDevice(nsPath string, cfg *DeviceConfig) error {
	if cfg.HostDevice == nil {
		return errors.New("host-device state is missing")
	}
	link, err := netlink.LinkByName(cfg.HostDevice.OriginalName)
	if err != nil {
		return fmt.Errorf("find host device %s: %w", cfg.HostDevice.OriginalName, err)
	}
	if cfg.Identity.PCIAddress != "" && interfaceIdentity(cfg.HostDevice.OriginalName).PCIAddress != cfg.Identity.PCIAddress {
		return fmt.Errorf("host device %s PCI identity changed", cfg.HostDevice.OriginalName)
	}
	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		return fmt.Errorf("open Pod network namespace: %w", err)
	}
	defer target.Close()
	if err := netlink.LinkSetDown(link); err != nil {
		return fmt.Errorf("set host device %s down: %w", cfg.HostDevice.OriginalName, err)
	}
	if err := netlink.LinkSetNsFd(link, int(target)); err != nil {
		_ = restoreLinkState(link, cfg.HostDevice)
		return fmt.Errorf("move host device %s into Pod: %w", cfg.HostDevice.OriginalName, err)
	}
	err = inNetNS(target, func() error {
		podLink, err := findLink(cfg.HostDevice.OriginalName, cfg.HostDevice.OriginalMAC)
		if err != nil {
			return err
		}
		if cfg.Network.InterfaceName != "" && cfg.Network.InterfaceName != podLink.Attrs().Name {
			if err := netlink.LinkSetName(podLink, cfg.Network.InterfaceName); err != nil {
				return fmt.Errorf("rename host device in Pod: %w", err)
			}
			podLink, err = netlink.LinkByName(cfg.Network.InterfaceName)
			if err != nil {
				return err
			}
		}
		if cfg.Network.MTU > 0 && cfg.Network.MTU != podLink.Attrs().MTU {
			if err := netlink.LinkSetMTU(podLink, cfg.Network.MTU); err != nil {
				return err
			}
		}
		for _, address := range cfg.Network.Addresses {
			parsed, err := netlink.ParseAddr(address)
			if err != nil {
				return err
			}
			if err := netlink.AddrAdd(podLink, parsed); err != nil && !os.IsExist(err) {
				return err
			}
		}
		if err := netlink.LinkSetUp(podLink); err != nil {
			return err
		}
		cfg.AttachedIf = podLink.Attrs().Name
		cfg.HardwareAddress = podLink.Attrs().HardwareAddr.String()
		return nil
	})
	if err != nil {
		if restoreErr := restoreHostDevice(nsPath, cfg); restoreErr != nil {
			return fmt.Errorf("attach host device: %v; rollback failed: %w", err, restoreErr)
		}
		return err
	}
	cfg.HostDevice.Restored = false
	cfg.LifecycleState = "Attached"
	return nil
}

func restoreHostDevice(nsPath string, cfg *DeviceConfig) error {
	if cfg.HostDevice == nil || cfg.HostDevice.Restored {
		return nil
	}
	if link, err := findLink(cfg.HostDevice.OriginalName, cfg.HostDevice.OriginalMAC); err == nil {
		if err := restoreLinkState(link, cfg.HostDevice); err != nil {
			return err
		}
		cfg.HostDevice.Restored = true
		cfg.AttachedIf = ""
		cfg.LifecycleState = "Restored"
		return nil
	}
	if nsPath == "" {
		return fmt.Errorf("host device %s is not in the host namespace and Pod namespace is unavailable", cfg.HostDevice.OriginalName)
	}
	rootNS, err := netns.Get()
	if err != nil {
		return err
	}
	defer rootNS.Close()
	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		return fmt.Errorf("open Pod network namespace for host-device restore: %w", err)
	}
	defer target.Close()
	err = inNetNS(target, func() error {
		link, err := findLink(cfg.AttachedIf, cfg.HostDevice.OriginalMAC)
		if err != nil {
			return err
		}
		if err := netlink.LinkSetDown(link); err != nil {
			return err
		}
		if link.Attrs().Name != cfg.HostDevice.OriginalName {
			if err := netlink.LinkSetName(link, cfg.HostDevice.OriginalName); err != nil {
				return err
			}
			link, err = netlink.LinkByName(cfg.HostDevice.OriginalName)
			if err != nil {
				return err
			}
		}
		return netlink.LinkSetNsFd(link, int(rootNS))
	})
	if err != nil {
		return fmt.Errorf("move host device back to host: %w", err)
	}
	link, err := netlink.LinkByName(cfg.HostDevice.OriginalName)
	if err != nil {
		return fmt.Errorf("find restored host device: %w", err)
	}
	if err := restoreLinkState(link, cfg.HostDevice); err != nil {
		return err
	}
	cfg.HostDevice.Restored = true
	cfg.AttachedIf = ""
	cfg.LifecycleState = "Restored"
	return nil
}

func (d *Driver) validateHostDeviceSafety(ctx context.Context, link netlink.Link, state *HostDeviceState) error {
	if link.Attrs().MasterIndex != 0 {
		return fmt.Errorf("host device %s belongs to link master index %d; set allowUnsafe only after verifying node connectivity", link.Attrs().Name, link.Attrs().MasterIndex)
	}
	routes, err := netlink.RouteList(link, 0)
	if err != nil {
		return fmt.Errorf("inspect routes for host device %s: %w", link.Attrs().Name, err)
	}
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "0.0.0.0/0" || route.Dst.String() == "::/0" {
			return fmt.Errorf("host device %s carries a default route; refusing exclusive allocation", link.Attrs().Name)
		}
	}
	node, err := d.client.CoreV1().Nodes().Get(ctx, d.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("inspect node addresses before host-device allocation: %w", err)
	}
	for _, nodeAddress := range node.Status.Addresses {
		if nodeAddress.Type != corev1.NodeInternalIP && nodeAddress.Type != corev1.NodeExternalIP {
			continue
		}
		for _, cidr := range state.OriginalAddresses {
			ip, _, parseErr := net.ParseCIDR(cidr)
			if parseErr == nil && ip.String() == nodeAddress.Address {
				return fmt.Errorf("host device %s carries Kubernetes node address %s; refusing exclusive allocation", link.Attrs().Name, nodeAddress.Address)
			}
		}
	}
	return nil
}

func restoreLinkState(link netlink.Link, state *HostDeviceState) error {
	if link.Attrs().Name != state.OriginalName {
		if err := netlink.LinkSetName(link, state.OriginalName); err != nil {
			return fmt.Errorf("restore interface name: %w", err)
		}
		var err error
		link, err = netlink.LinkByName(state.OriginalName)
		if err != nil {
			return err
		}
	}
	if err := netlink.LinkSetDown(link); err != nil {
		return err
	}
	if state.OriginalMAC != "" && !strings.EqualFold(link.Attrs().HardwareAddr.String(), state.OriginalMAC) {
		mac, err := net.ParseMAC(state.OriginalMAC)
		if err != nil {
			return err
		}
		if err := netlink.LinkSetHardwareAddr(link, mac); err != nil {
			return err
		}
	}
	if state.OriginalMTU > 0 && link.Attrs().MTU != state.OriginalMTU {
		if err := netlink.LinkSetMTU(link, state.OriginalMTU); err != nil {
			return err
		}
	}
	currentAddresses, err := netlink.AddrList(link, 0)
	if err != nil {
		return err
	}
	for _, address := range currentAddresses {
		_ = netlink.AddrDel(link, &address)
	}
	addressState := state.AddressState
	if len(addressState) == 0 {
		for _, cidr := range state.OriginalAddresses {
			addressState = append(addressState, HostAddressState{CIDR: cidr})
		}
	}
	for _, saved := range addressState {
		parsed, err := netlink.ParseAddr(saved.CIDR)
		if err != nil {
			return err
		}
		parsed.Label = saved.Label
		parsed.Flags = saved.Flags
		parsed.Scope = saved.Scope
		parsed.PreferedLft = saved.PreferredLft
		parsed.ValidLft = saved.ValidLft
		if saved.Peer != "" {
			parsed.Peer, err = netlink.ParseIPNet(saved.Peer)
			if err != nil {
				return err
			}
		}
		if saved.Broadcast != "" {
			parsed.Broadcast = net.ParseIP(saved.Broadcast)
		}
		if err := netlink.AddrAdd(link, parsed); err != nil && !os.IsExist(err) {
			return err
		}
	}
	if state.OriginalAdminUp {
		return netlink.LinkSetUp(link)
	}
	return netlink.LinkSetDown(link)
}

func findLink(preferredName, hardwareAddress string) (netlink.Link, error) {
	if preferredName != "" {
		if link, err := netlink.LinkByName(preferredName); err == nil {
			return link, nil
		}
	}
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		if hardwareAddress != "" && strings.EqualFold(link.Attrs().HardwareAddr.String(), hardwareAddress) {
			return link, nil
		}
	}
	return nil, fmt.Errorf("interface %q with MAC %q not found", preferredName, hardwareAddress)
}

func (d *Driver) restorePodHostDevices(ctx context.Context, pod *api.PodSandbox) error {
	podUID := types.UID(pod.GetUid())
	podCfg, ok := d.store.GetPod(podUID)
	if !ok {
		return nil
	}
	nsPath := networkNamespace(pod)
	if nsPath == "" {
		nsPath = podCfg.NetNS
	}
	var restoreErrors []error
	for deviceName, cfg := range podCfg.Devices {
		if cfg.Network.Type != "host-device" || cfg.HostDevice == nil || cfg.HostDevice.Restored {
			continue
		}
		if err := restoreHostDevice(nsPath, &cfg); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore %s: %w", cfg.ParentName, err))
			continue
		}
		if err := d.store.SetDevice(podUID, deviceName, cfg); err != nil {
			restoreErrors = append(restoreErrors, err)
			continue
		}
		message := networkRestoredMessage(cfg)
		_ = d.setResourceClaimDeviceStatus(ctx, cfg, false, "NetworkRestored", message, cfg.HardwareAddress)
		if kubePod, err := d.client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil && kubePod.UID == podUID {
			_ = d.setPodNetworkStatus(ctx, kubePod.Namespace, kubePod.Name, kubePod.UID, cfg)
			_ = d.emitPodEvent(ctx, kubePod, corev1.EventTypeNormal, "LinuxHostDeviceRestored", "RestoreHostDevice", message, &cfg.Claim)
		} else if err != nil && !apierrors.IsNotFound(err) {
			klog.ErrorS(err, "Could not get Pod while reporting host-device restore")
		}
	}
	_ = d.publish(ctx)
	return errors.Join(restoreErrors...)
}

func (d *Driver) restoreClaimHostDevices(ctx context.Context, claimUID types.UID) error {
	var restoreErrors []error
	for podUID, podCfg := range d.store.PodConfigs() {
		for deviceName, cfg := range podCfg.Devices {
			if cfg.ClaimUID != claimUID || cfg.Network.Type != "host-device" || cfg.HostDevice == nil || cfg.HostDevice.Restored {
				continue
			}
			if err := restoreHostDevice(podCfg.NetNS, &cfg); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("restore %s: %w", cfg.ParentName, err))
				continue
			}
			if err := d.store.SetDevice(podUID, deviceName, cfg); err != nil {
				restoreErrors = append(restoreErrors, err)
				continue
			}
			message := networkRestoredMessage(cfg)
			_ = d.setResourceClaimDeviceStatus(ctx, cfg, false, "NetworkRestored", message, cfg.HardwareAddress)
		}
	}
	_ = d.publish(ctx)
	return errors.Join(restoreErrors...)
}
