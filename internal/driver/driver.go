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
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"

	"github.com/infinitydon/dra-linux-networks/internal/config"
	"github.com/infinitydon/dra-linux-networks/internal/ipamapi"
)

type Options struct {
	NodeName               string
	DriverName             string
	KubeletPluginsDir      string
	KubeletRegistrationDir string
	StateFile              string
	Config                 *config.Config
	Client                 kubernetes.Interface
	DynamicClient          dynamic.Interface
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
	dynamic    dynamic.Interface
	helper     helper
	nri        stub.Stub
	store      *Store
	devices    map[string]config.InterfaceConfig
	ipPools    map[string]config.IPPool
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
	if opts.DynamicClient == nil {
		return nil, fmt.Errorf("dynamic Kubernetes client is required")
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
		dynamic:    opts.DynamicClient,
		store:      store,
		devices:    map[string]config.InterfaceConfig{},
		ipPools:    map[string]config.IPPool{},
	}

	for _, ifc := range opts.Config.Interfaces {
		d.devices[deviceName(ifc.Name)] = ifc
	}
	d.reloadIPPools(ctx)

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
		allowMultiple := true
		devices = append(devices, resourceapi.Device{
			Name:                     deviceName(ifc.Name),
			Attributes:               deviceAttributes(ifc, link),
			AllowMultipleAllocations: &allowMultiple,
		})
	}
	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			d.nodeName: {Slices: []resourceslice.Slice{{Devices: devices}}},
		},
	}
	return d.helper.PublishResources(ctx, resources)
}

func (d *Driver) reloadIPPools(ctx context.Context) {
	pools := map[string]config.IPPool{}
	for _, pool := range d.cfg.IPPools {
		pools[pool.Name] = pool
	}
	if d.dynamic != nil {
		list, err := d.dynamic.Resource(ipamapi.IPPoolGVR).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			klog.InfoS("IPPool CRD is not installed, using configured IP pools")
		} else if err != nil {
			klog.ErrorS(err, "Could not list IPPool resources, using configured IP pools")
		} else {
			for i := range list.Items {
				pool, err := ipPoolFromUnstructured(&list.Items[i])
				if err != nil {
					klog.ErrorS(err, "Ignoring invalid IPPool", "name", list.Items[i].GetName())
					continue
				}
				pools[pool.Name] = pool
			}
		}
	}
	d.ipPools = pools
}

func ipPoolFromUnstructured(obj *unstructured.Unstructured) (config.IPPool, error) {
	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		return config.IPPool{}, fmt.Errorf("spec is required")
	}
	pool := config.IPPool{Name: obj.GetName()}
	if value, ok, _ := unstructured.NestedString(spec, "subnet"); ok {
		pool.Subnet = value
	}
	if value, ok, _ := unstructured.NestedString(spec, "gateway"); ok {
		pool.Gateway = value
	}
	pool.Allocations = ipRangesFromSpec(spec, "allocations")
	pool.Reservations = ipRangesFromSpec(spec, "reservations")
	if rawRoutes, ok, _ := unstructured.NestedSlice(spec, "routes"); ok {
		for _, rawRoute := range rawRoutes {
			routeMap, ok := rawRoute.(map[string]interface{})
			if !ok {
				continue
			}
			route := config.Route{}
			if value, ok, _ := unstructured.NestedString(routeMap, "destination"); ok {
				route.Destination = value
			}
			if value, ok, _ := unstructured.NestedString(routeMap, "gateway"); ok {
				route.Gateway = value
			}
			pool.Routes = append(pool.Routes, route)
		}
	}
	if pool.Subnet == "" {
		return config.IPPool{}, fmt.Errorf("spec.subnet is required")
	}
	if len(pool.Allocations) == 0 {
		return config.IPPool{}, fmt.Errorf("spec.allocations is required")
	}
	return pool, nil
}

func ipRangesFromSpec(spec map[string]interface{}, field string) []config.IPRange {
	rawRanges, ok, _ := unstructured.NestedSlice(spec, field)
	if !ok {
		return nil
	}
	ranges := []config.IPRange{}
	for _, rawRange := range rawRanges {
		rangeMap, ok := rawRange.(map[string]interface{})
		if !ok {
			continue
		}
		ipRange := config.IPRange{}
		if value, ok, _ := unstructured.NestedString(rangeMap, "name"); ok {
			ipRange.Name = value
		}
		if value, ok, _ := unstructured.NestedString(rangeMap, "rangeStart"); ok {
			ipRange.RangeStart = value
		}
		if value, ok, _ := unstructured.NestedString(rangeMap, "rangeEnd"); ok {
			ipRange.RangeEnd = value
		}
		if values, ok, _ := unstructured.NestedStringSlice(rangeMap, "addresses"); ok {
			ipRange.Addresses = values
		}
		ranges = append(ranges, ipRange)
	}
	return ranges
}

func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	result := map[types.UID]kubeletplugin.PrepareResult{}
	for _, claim := range claims {
		result[claim.UID] = d.prepareClaim(ctx, claim)
	}
	return result, nil
}

func podStaticAddress(annotations map[string]string, request, interfaceName, expectedPool string) (string, error) {
	if len(annotations) == 0 {
		return "", nil
	}
	bases := []string{
		AttrPrefix + "/" + request,
		AttrPrefix + "/" + interfaceName,
	}
	for _, base := range bases {
		address := strings.TrimSpace(annotations[base+".address"])
		pool := strings.TrimSpace(annotations[base+".ip-pool"])
		if address == "" && pool == "" {
			continue
		}
		if address == "" || pool == "" {
			return "", fmt.Errorf("pod static network annotations %s.address and %s.ip-pool must be set together", base, base)
		}
		if expectedPool == "" {
			return "", fmt.Errorf("pod static network annotation %s.address requires ipPool in the ResourceClaim configuration", base)
		}
		if pool != expectedPool {
			return "", fmt.Errorf("pod static network annotation %s.ip-pool=%q does not match ResourceClaim ipPool=%q", base, pool, expectedPool)
		}
		return address, nil
	}
	return "", nil
}

func (d *Driver) prepareClaim(ctx context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
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
	pod, err := d.client.CoreV1().Pods(claim.Namespace).Get(ctx, reserved.Name, metav1.GetOptions{})
	if err != nil {
		return kubeletplugin.PrepareResult{Err: fmt.Errorf("get pod %s/%s for claim %s/%s: %w", claim.Namespace, reserved.Name, claim.Namespace, claim.Name, err)}
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
		address, err := podStaticAddress(pod.Annotations, allocation.Request, netCfg.InterfaceName, netCfg.IPPool)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		if address != "" {
			netCfg.Address = address
		}
		if err := validateConfig(ifc, netCfg); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		if err := d.applyIPAM(ctx, &netCfg, claim, pod); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}

		var shareID *string
		if allocation.ShareID != nil {
			value := string(*allocation.ShareID)
			shareID = &value
		}
		deviceCfg := DeviceConfig{
			Claim:      types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name},
			ClaimUID:   claim.UID,
			DriverName: allocation.Driver,
			PoolName:   allocation.Pool,
			DeviceName: allocation.Device,
			ShareID:    shareID,
			ParentName: ifc.Name,
			Network:    netCfg,
		}
		if err := d.store.SetDevice(types.UID(reserved.UID), allocation.Device, deviceCfg); err != nil {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("persist pod config: %w", err)}
		}
		preparedMessage := networkPreparedMessage(deviceCfg)
		if err := d.setResourceClaimDeviceStatus(ctx, deviceCfg, false, "NetworkPrepared", preparedMessage, ""); err != nil {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("report prepared device status: %w", err)}
		}
		if err := d.emitPodEvent(ctx, pod, corev1.EventTypeNormal, "LinuxNetworkPrepared", "PrepareNetwork", preparedMessage, &deviceCfg.Claim); err != nil {
			klog.ErrorS(err, "Could not emit Pod event", "pod", klog.KObj(pod))
		}
		prepared = append(prepared, kubeletplugin.Device{
			Requests:   []string{allocation.Request},
			PoolName:   d.nodeName,
			DeviceName: allocation.Device,
		})
	}
	if len(prepared) > 0 {
		if err := d.setPodNetworkCondition(ctx, pod.Namespace, pod.Name, pod.UID, corev1.ConditionFalse, "NetworkPrepared", "Secondary network resources are prepared; waiting for attachment"); err != nil {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("set Pod network readiness: %w", err)}
		}
	}
	return kubeletplugin.PrepareResult{Devices: prepared}
}

func (d *Driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	result := map[types.UID]error{}
	for _, claim := range claims {
		if err := d.removeResourceClaimDeviceStatus(ctx, types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name}); err != nil {
			result[claim.UID] = fmt.Errorf("remove ResourceClaim device status: %w", err)
			continue
		}
		if err := ipamapi.DeleteForClaim(ctx, d.dynamic, claim.UID); err != nil {
			result[claim.UID] = fmt.Errorf("delete cluster IP allocation: %w", err)
			continue
		}
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

func (d *Driver) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
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
	kubePod, err := d.client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get Pod for network status: %w", err)
	}
	if kubePod.UID != podUID {
		return fmt.Errorf("Pod %s/%s UID changed", pod.Namespace, pod.Name)
	}
	for deviceName, cfg := range podCfg.Devices {
		if cfg.AttachedIf == "" {
			hardwareAddress, err := attachLink(nsPath, cfg)
			if err != nil {
				message := fmt.Sprintf("failed to attach %s: %v", cfg.Network.InterfaceName, err)
				_ = d.setResourceClaimDeviceStatus(ctx, cfg, false, "NetworkAttachFailed", message, "")
				_ = d.setPodNetworkCondition(ctx, kubePod.Namespace, kubePod.Name, kubePod.UID, corev1.ConditionFalse, "NetworkAttachFailed", message)
				_ = d.emitPodEvent(ctx, kubePod, corev1.EventTypeWarning, "LinuxNetworkAttachFailed", "AttachNetwork", message, &cfg.Claim)
				return fmt.Errorf("attach %s for pod %s/%s: %w", deviceName, pod.Namespace, pod.Name, err)
			}
			cfg.AttachedIf = cfg.Network.InterfaceName
			cfg.HardwareAddress = hardwareAddress
			if err := d.store.SetDevice(podUID, deviceName, cfg); err != nil {
				return err
			}
		}
		message := networkStatusMessage(cfg)
		if err := d.setResourceClaimDeviceStatus(ctx, cfg, true, "NetworkAttached", message, cfg.HardwareAddress); err != nil {
			_ = d.emitPodEvent(ctx, kubePod, corev1.EventTypeWarning, "LinuxNetworkStatusUpdateFailed", "ReportNetworkStatus", err.Error(), &cfg.Claim)
			return fmt.Errorf("report attached device status: %w", err)
		}
		if err := d.emitPodEvent(ctx, kubePod, corev1.EventTypeNormal, "LinuxNetworkAttached", "AttachNetwork", message, &cfg.Claim); err != nil {
			klog.ErrorS(err, "Could not emit Pod event", "pod", klog.KObj(kubePod))
		}
	}
	if err := d.setPodNetworkCondition(ctx, kubePod.Namespace, kubePod.Name, kubePod.UID, corev1.ConditionTrue, "NetworkAttached", "All requested secondary networks are attached"); err != nil {
		return fmt.Errorf("set Pod network readiness: %w", err)
	}
	return nil
}

func (d *Driver) StopPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.InfoS("pod sandbox stopped", "namespace", pod.GetNamespace(), "name", pod.GetName(), "uid", pod.GetUid())
	return nil
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

func attachLink(nsPath string, cfg DeviceConfig) (string, error) {
	parent, err := netlink.LinkByName(cfg.ParentName)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("unsupported link type %q", cfg.Network.Type)
	}

	if old, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(old)
	}
	if err := netlink.LinkAdd(child); err != nil {
		return "", err
	}

	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		_ = netlink.LinkDel(child)
		return "", err
	}
	defer target.Close()

	if err := netlink.LinkSetNsFd(child, int(target)); err != nil {
		_ = netlink.LinkDel(child)
		return "", err
	}

	hardwareAddress := ""
	err = inNetNS(target, func() error {
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
		hardwareAddress = link.Attrs().HardwareAddr.String()
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
	return hardwareAddress, err
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
	setBool(AttrMacvlan, slices.Contains(ifc.Types, "macvlan"))
	setBool(AttrIPvlan, slices.Contains(ifc.Types, "ipvlan"))
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
	if src.IPPool != "" {
		dst.IPPool = src.IPPool
	}
	if src.Address != "" {
		dst.Address = src.Address
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
