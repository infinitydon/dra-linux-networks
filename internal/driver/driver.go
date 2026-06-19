package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

type Options struct {
	NodeName               string
	DriverName             string
	KubeletPluginsDir      string
	KubeletRegistrationDir string
	StateFile              string
	Config                 *config.Config
	Client                 kubernetes.Interface
}

type helper interface {
	PublishResources(context.Context, resourceslice.DriverResources) error
	RegistrationStatus() *registerapi.RegistrationStatus
	Stop()
}

type Driver struct {
	nodeName   string
	driverName string
	cfg        *config.Config
	client     kubernetes.Interface
	helper     helper
	nri        stub.Stub
	store      *Store
	devices    map[string]config.InterfaceConfig
}

func Start(ctx context.Context, opts Options) (*Driver, error) {
	if opts.DriverName == "" {
		opts.DriverName = config.DefaultDriverName
	}
	if opts.KubeletPluginsDir == "" {
		opts.KubeletPluginsDir = kubeletplugin.KubeletPluginsDir
	}
	if opts.KubeletRegistrationDir == "" {
		opts.KubeletRegistrationDir = kubeletplugin.KubeletRegistryDir
	}
	if opts.StateFile == "" {
		opts.StateFile = "/var/run/linux-net-dra/state.json"
	}
	if opts.Config == nil {
		return nil, fmt.Errorf("config is required")
	}

	store, err := NewStore(opts.StateFile)
	if err != nil {
		return nil, fmt.Errorf("open state file: %w", err)
	}

	d := &Driver{
		nodeName:   opts.NodeName,
		driverName: opts.DriverName,
		cfg:        opts.Config,
		client:     opts.Client,
		store:      store,
		devices:    map[string]config.InterfaceConfig{},
	}

	for _, ifc := range opts.Config.Interfaces {
		d.devices[deviceName(ifc.Name)] = ifc
	}

	pluginPath := filepath.Join(opts.KubeletPluginsDir, opts.DriverName)
	if err := os.MkdirAll(pluginPath, 0750); err != nil {
		return nil, fmt.Errorf("create plugin path %s: %w", pluginPath, err)
	}

	h, err := kubeletplugin.Start(ctx, d,
		kubeletplugin.DriverName(opts.DriverName),
		kubeletplugin.NodeName(opts.NodeName),
		kubeletplugin.KubeClient(opts.Client),
		kubeletplugin.PluginDataDirectoryPath(pluginPath),
		kubeletplugin.RegistrarDirectoryPath(opts.KubeletRegistrationDir),
	)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	d.helper = h

	if err := d.publish(ctx); err != nil {
		h.Stop()
		return nil, err
	}

	nri, err := stub.New(d,
		stub.WithPluginName(opts.DriverName),
		stub.WithPluginIdx("00"),
		stub.WithOnClose(func() { klog.Infof("NRI plugin %s closed", opts.DriverName) }),
	)
	if err != nil {
		h.Stop()
		return nil, fmt.Errorf("create NRI stub: %w", err)
	}
	d.nri = nri

	go func() {
		for {
			if err := d.nri.Run(ctx); err != nil {
				klog.ErrorS(err, "NRI plugin exited")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	return d, nil
}

func (d *Driver) Stop() {
	if d.nri != nil {
		d.nri.Stop()
	}
	if d.helper != nil {
		d.helper.Stop()
	}
}

func (d *Driver) HandleError(ctx context.Context, err error, msg string) {
	utilruntime.HandleErrorWithContext(ctx, err, msg)
}

func (d *Driver) publish(ctx context.Context) error {
	devices := []resourceapi.Device{}
	for _, ifc := range d.cfg.Interfaces {
		link, err := netlink.LinkByName(ifc.Name)
		if err != nil {
			klog.ErrorS(err, "configured interface is not present, skipping", "interface", ifc.Name)
			continue
		}
		devices = append(devices, resourceapi.Device{
			Name:       deviceName(ifc.Name),
			Attributes: deviceAttributes(ifc, link),
		})
	}
	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			d.nodeName: {Slices: []resourceslice.Slice{{Devices: devices}}},
		},
	}
	return d.helper.PublishResources(ctx, resources)
}

func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	result := map[types.UID]kubeletplugin.PrepareResult{}
	for _, claim := range claims {
		result[claim.UID] = d.prepareClaim(ctx, claim)
	}
	return result, nil
}

func (d *Driver) prepareClaim(_ context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	if len(claim.Status.ReservedFor) == 0 {
		return kubeletplugin.PrepareResult{}
	}
	if len(claim.Status.ReservedFor) > 1 {
		return kubeletplugin.PrepareResult{Err: fmt.Errorf("only one pod may reserve claim %s/%s", claim.Namespace, claim.Name)}
	}
	reserved := claim.Status.ReservedFor[0]
	if reserved.APIGroup != "" || reserved.Resource != "pods" {
		return kubeletplugin.PrepareResult{Err: fmt.Errorf("claim %s/%s is reserved for unsupported object %s/%s", claim.Namespace, claim.Name, reserved.APIGroup, reserved.Resource)}
	}

	prepared := []kubeletplugin.Device{}
	for _, allocation := range claim.Status.Allocation.Devices.Results {
		if allocation.Driver != d.driverName {
			continue
		}
		ifc, ok := d.devices[allocation.Device]
		if !ok {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("allocated unknown device %q", allocation.Device)}
		}

		netCfg := NetworkConfig{
			Type:          ifc.DefaultType,
			Mode:          ifc.DefaultMode,
			InterfaceName: ifc.DefaultPodName,
			MTU:           ifc.MTU,
		}
		if userCfg, err := d.configForRequest(claim, allocation.Request); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		} else {
			mergeConfig(&netCfg, userCfg)
		}
		if err := validateConfig(ifc, netCfg); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}

		deviceCfg := DeviceConfig{
			Claim:      types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name},
			ClaimUID:   claim.UID,
			DeviceName: allocation.Device,
			ParentName: ifc.Name,
			Network:    netCfg,
		}
		if err := d.store.SetDevice(types.UID(reserved.UID), allocation.Device, deviceCfg); err != nil {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("persist pod config: %w", err)}
		}
		prepared = append(prepared, kubeletplugin.Device{
			Requests:   []string{allocation.Request},
			PoolName:   d.nodeName,
			DeviceName: allocation.Device,
		})
	}
	return kubeletplugin.PrepareResult{Devices: prepared}
}

func (d *Driver) UnprepareResourceClaims(_ context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	result := map[types.UID]error{}
	for _, claim := range claims {
		result[claim.UID] = d.store.DeleteClaim(claim.UID)
	}
	return result, nil
}

func (d *Driver) Synchronize(_ context.Context, pods []*api.PodSandbox, _ []*api.Container) ([]*api.ContainerUpdate, error) {
	for _, pod := range pods {
		if ns := networkNamespace(pod); ns != "" {
			_ = d.store.SetNetNS(types.UID(pod.Uid), ns)
		}
	}
	return nil, nil
}

func (d *Driver) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	podUID := types.UID(pod.GetUid())
	podCfg, ok := d.store.GetPod(podUID)
	if !ok {
		return nil
	}
	nsPath := networkNamespace(pod)
	if nsPath == "" {
		return fmt.Errorf("pod %s/%s has no network namespace", pod.Namespace, pod.Name)
	}
	if err := d.store.SetNetNS(podUID, nsPath); err != nil {
		return err
	}
	for deviceName, cfg := range podCfg.Devices {
		if cfg.AttachedIf != "" {
			continue
		}
		if err := attachLink(nsPath, cfg); err != nil {
			return fmt.Errorf("attach %s for pod %s/%s: %w", deviceName, pod.Namespace, pod.Name, err)
		}
		cfg.AttachedIf = cfg.Network.InterfaceName
		if err := d.store.SetDevice(podUID, deviceName, cfg); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) StopPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	return d.store.DeletePod(types.UID(pod.GetUid()))
}

func (d *Driver) configForRequest(claim *resourceapi.ResourceClaim, request string) (NetworkConfig, error) {
	cfg := NetworkConfig{}
	for _, item := range claim.Status.Allocation.Devices.Config {
		if item.Opaque == nil || item.Opaque.Driver != d.driverName {
			continue
		}
		if len(item.Requests) > 0 && !slices.Contains(item.Requests, request) {
			continue
		}
		if len(item.Opaque.Parameters.Raw) == 0 {
			continue
		}
		if err := json.Unmarshal(item.Opaque.Parameters.Raw, &cfg); err != nil {
			return cfg, fmt.Errorf("decode claim config for %s/%s request %s: %w", claim.Namespace, claim.Name, request, err)
		}
		break
	}
	return cfg, nil
}

func attachLink(nsPath string, cfg DeviceConfig) error {
	parent, err := netlink.LinkByName(cfg.ParentName)
	if err != nil {
		return err
	}
	name := hostChildName(cfg)
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.ParentIndex = parent.Attrs().Index
	if cfg.Network.MTU > 0 {
		attrs.MTU = cfg.Network.MTU
	}

	var child netlink.Link
	switch cfg.Network.Type {
	case "macvlan":
		child = &netlink.Macvlan{LinkAttrs: attrs, Mode: macvlanMode(cfg.Network.Mode)}
	case "ipvlan":
		child = &netlink.IPVlan{LinkAttrs: attrs, Mode: ipvlanMode(cfg.Network.Mode)}
	default:
		return fmt.Errorf("unsupported link type %q", cfg.Network.Type)
	}

	if old, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(old)
	}
	if err := netlink.LinkAdd(child); err != nil {
		return err
	}

	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		_ = netlink.LinkDel(child)
		return err
	}
	defer target.Close()

	if err := netlink.LinkSetNsFd(child, int(target)); err != nil {
		_ = netlink.LinkDel(child)
		return err
	}

	return inNetNS(target, func() error {
		link, err := netlink.LinkByName(name)
		if err != nil {
			return err
		}
		if cfg.Network.InterfaceName != "" && cfg.Network.InterfaceName != name {
			if err := netlink.LinkSetName(link, cfg.Network.InterfaceName); err != nil {
				return err
			}
			link, err = netlink.LinkByName(cfg.Network.InterfaceName)
			if err != nil {
				return err
			}
		}
		for _, address := range cfg.Network.Addresses {
			addr, err := netlink.ParseAddr(address)
			if err != nil {
				return err
			}
			if err := netlink.AddrAdd(link, addr); err != nil && !os.IsExist(err) {
				return err
			}
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return err
		}
		if cfg.Network.Gateway != "" {
			if err := addRoute(link, "0.0.0.0/0", cfg.Network.Gateway); err != nil {
				return err
			}
		}
		for _, route := range cfg.Network.Routes {
			if err := addRoute(link, route.Destination, route.Gateway); err != nil {
				return err
			}
		}
		return nil
	})
}

func addRoute(link netlink.Link, dst, gw string) error {
	route := netlink.Route{LinkIndex: link.Attrs().Index}
	if dst != "" {
		_, ipnet, err := net.ParseCIDR(dst)
		if err != nil {
			return err
		}
		route.Dst = ipnet
	}
	if gw != "" {
		route.Gw = net.ParseIP(gw)
	}
	return netlink.RouteAdd(&route)
}

func inNetNS(target netns.NsHandle, fn func() error) error {
	current, err := netns.Get()
	if err != nil {
		return err
	}
	defer current.Close()
	if err := netns.Set(target); err != nil {
		return err
	}
	defer func() { _ = netns.Set(current) }()
	return fn()
}

func networkNamespace(pod *api.PodSandbox) string {
	if pod == nil || pod.Linux == nil || pod.Linux.Namespaces == nil {
		return ""
	}
	for _, ns := range pod.Linux.Namespaces {
		if ns.Type == "network" {
			return ns.Path
		}
	}
	return ""
}

func deviceName(ifName string) string {
	return strings.ReplaceAll(ifName, ".", "-")
}

func hostChildName(cfg DeviceConfig) string {
	base := "lnd" + strings.ReplaceAll(string(cfg.ClaimUID), "-", "")
	if len(base) > 12 {
		base = base[:12]
	}
	return base
}

func deviceAttributes(ifc config.InterfaceConfig, link netlink.Link) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}
	setString := func(name, value string) {
		attrs[resourceapi.QualifiedName(name)] = resourceapi.DeviceAttribute{StringValue: &value}
	}
	setBool := func(name string, value bool) {
		attrs[resourceapi.QualifiedName(name)] = resourceapi.DeviceAttribute{BoolValue: &value}
	}
	setInt := func(name string, value int64) {
		attrs[resourceapi.QualifiedName(name)] = resourceapi.DeviceAttribute{IntValue: &value}
	}
	setString(AttrInterface, ifc.Name)
	setString(AttrMAC, link.Attrs().HardwareAddr.String())
	setString(AttrTypes, strings.Join(ifc.Types, ","))
	setString(AttrMacvlanModes, strings.Join(ifc.MacvlanModes, ","))
	setString(AttrIPvlanModes, strings.Join(ifc.IPvlanModes, ","))
	setBool(AttrDefault, ifc.Default)
	setInt(AttrMTU, int64(link.Attrs().MTU))
	return attrs
}

func mergeConfig(dst *NetworkConfig, src NetworkConfig) {
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.InterfaceName != "" {
		dst.InterfaceName = src.InterfaceName
	}
	if src.MTU > 0 {
		dst.MTU = src.MTU
	}
	if len(src.Addresses) > 0 {
		dst.Addresses = src.Addresses
	}
	if src.Gateway != "" {
		dst.Gateway = src.Gateway
	}
	if len(src.Routes) > 0 {
		dst.Routes = src.Routes
	}
}

func validateConfig(ifc config.InterfaceConfig, cfg NetworkConfig) error {
	if !slices.Contains(ifc.Types, cfg.Type) {
		return fmt.Errorf("interface %s does not allow type %q", ifc.Name, cfg.Type)
	}
	switch cfg.Type {
	case "macvlan":
		if !slices.Contains(ifc.MacvlanModes, cfg.Mode) {
			return fmt.Errorf("interface %s does not allow macvlan mode %q", ifc.Name, cfg.Mode)
		}
	case "ipvlan":
		if !slices.Contains(ifc.IPvlanModes, cfg.Mode) {
			return fmt.Errorf("interface %s does not allow ipvlan mode %q", ifc.Name, cfg.Mode)
		}
	}
	return nil
}

func macvlanMode(mode string) netlink.MacvlanMode {
	switch mode {
	case "private":
		return netlink.MACVLAN_MODE_PRIVATE
	case "vepa":
		return netlink.MACVLAN_MODE_VEPA
	case "passthru":
		return netlink.MACVLAN_MODE_PASSTHRU
	default:
		return netlink.MACVLAN_MODE_BRIDGE
	}
}

func ipvlanMode(mode string) netlink.IPVlanMode {
	switch mode {
	case "l3":
		return netlink.IPVLAN_MODE_L3
	case "l3s":
		return netlink.IPVLAN_MODE_L3S
	default:
		return netlink.IPVLAN_MODE_L2
	}
}
