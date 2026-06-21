package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

type dpdkDevice struct {
	Name  string
	State DPDKDeviceState
}

func (d *Driver) prepareDPDKDevice(ctx context.Context, claim *resourceapi.ResourceClaim, pod *corev1.Pod, podUID types.UID, allocation resourceapi.DeviceRequestAllocationResult, device dpdkDevice) (kubeletplugin.Device, error) {
	userCfg, err := d.configForRequest(claim, allocation.Request)
	if err != nil {
		return kubeletplugin.Device{}, err
	}
	if err := validateDPDKConfig(userCfg); err != nil {
		return kubeletplugin.Device{}, fmt.Errorf("request %s: %w", allocation.Request, err)
	}
	state := device.State
	cdiID, specPath, err := writeCDISpec(d.cfg.DPDK, d.driverName, string(claim.UID), allocation.Device, state)
	if err != nil {
		return kubeletplugin.Device{}, fmt.Errorf("write CDI specification for %s: %w", state.PCIAddress, err)
	}
	state.CDIDeviceID = cdiID
	state.CDISpecPath = specPath
	cfg := DeviceConfig{
		Claim:            types.NamespacedName{Namespace: claim.Namespace, Name: claim.Name},
		ClaimUID:         claim.UID,
		DriverName:       allocation.Driver,
		PoolName:         allocation.Pool,
		DeviceName:       allocation.Device,
		ParentName:       state.PCIAddress,
		AllocationPolicy: "exclusive",
		Identity: InterfaceIdentity{
			KernelDriver: state.CurrentDriver,
			BusType:      "pci",
			PCIAddress:   state.PCIAddress,
			PCIVendorID:  state.VendorID,
			PCIDeviceID:  state.DeviceID,
		},
		DPDK:           &state,
		Network:        NetworkConfig{Type: "dpdk"},
		LifecycleState: "Prepared",
	}
	if err := d.store.SetDevice(podUID, allocation.Device, cfg); err != nil {
		_ = os.Remove(specPath)
		return kubeletplugin.Device{}, fmt.Errorf("persist DPDK device: %w", err)
	}
	message := dpdkPreparedMessage(cfg)
	if err := d.setResourceClaimDeviceStatus(ctx, cfg, true, "DPDKPrepared", message, ""); err != nil {
		return kubeletplugin.Device{}, fmt.Errorf("report DPDK device status: %w", err)
	}
	if err := d.setPodNetworkStatus(ctx, pod.Namespace, pod.Name, pod.UID, cfg); err != nil {
		return kubeletplugin.Device{}, fmt.Errorf("report Pod DPDK status: %w", err)
	}
	if err := d.emitPodEvent(ctx, pod, corev1.EventTypeNormal, "LinuxDPDKDevicePrepared", "PrepareDPDKDevice", message, &cfg.Claim); err != nil {
		klog.ErrorS(err, "Could not emit DPDK Pod event", "pod", klog.KObj(pod))
	}
	return kubeletplugin.Device{
		Requests:     []string{allocation.Request},
		PoolName:     d.nodeName,
		DeviceName:   allocation.Device,
		CDIDeviceIDs: []string{cdiID},
	}, nil
}

func validateDPDKConfig(cfg NetworkConfig) error {
	if cfg.Type != "" && cfg.Type != "dpdk" {
		return fmt.Errorf("DPDK device requires type dpdk, got %q", cfg.Type)
	}
	if cfg.Mode != "" || cfg.InterfaceName != "" || cfg.MTU != 0 || cfg.IPPool != "" || cfg.Address != "" || len(cfg.Addresses) > 0 || cfg.Gateway != "" || len(cfg.Routes) > 0 {
		return fmt.Errorf("DPDK devices do not support interface, MTU, mode, or IPAM configuration")
	}
	return nil
}

func discoverDPDKDevices(cfg config.DPDKConfig) (map[string]dpdkDevice, error) {
	devices := map[string]dpdkDevice{}
	if !cfg.Enabled {
		return devices, nil
	}
	names := loadPCIIDs(cfg.PCIIDPath)
	paths, err := filepath.Glob(filepath.Join(cfg.SysfsPath, "bus/pci/devices/*"))
	if err != nil {
		return nil, err
	}
	for _, devicePath := range paths {
		address := strings.ToLower(filepath.Base(devicePath))
		vendor := trimHexFile(filepath.Join(devicePath, "vendor"))
		device := trimHexFile(filepath.Join(devicePath, "device"))
		class := trimHexFile(filepath.Join(devicePath, "class"))
		driver := symlinkBase(filepath.Join(devicePath, "driver"))
		if !slices.Contains(cfg.Drivers, strings.ToLower(driver)) || !classAllowed(class, cfg.PCIClasses) {
			continue
		}
		if !selectorAllows(cfg.Include, address, vendor, device, true) || selectorAllows(cfg.Exclude, address, vendor, device, false) {
			continue
		}

		group := symlinkBase(filepath.Join(devicePath, "iommu_group"))
		mode, nodes := deviceNodes(cfg, devicePath, driver, group)
		if len(nodes) == 0 {
			continue
		}
		if driver == "vfio-pci" {
			members, _ := filepath.Glob(filepath.Join(cfg.SysfsPath, "kernel/iommu_groups", group, "devices/*"))
			if len(members) != 1 {
				// A VFIO group is the isolation boundary. Do not publish one function from a shared group.
				continue
			}
			if mode == "noiommu" && !cfg.AllowUnsafeNoIOMMU {
				continue
			}
		}
		compatible := compatibleKernelDrivers(cfg, devicePath, vendor, device, driver)
		state := DPDKDeviceState{
			PCIAddress:              address,
			PCIClass:                class,
			VendorID:                vendor,
			VendorName:              names["v:"+vendor],
			DeviceID:                device,
			DeviceName:              names["d:"+vendor+":"+device],
			SubsystemVendorID:       trimHexFile(filepath.Join(devicePath, "subsystem_vendor")),
			SubsystemDeviceID:       trimHexFile(filepath.Join(devicePath, "subsystem_device")),
			CurrentDriver:           driver,
			CompatibleKernelDrivers: compatible,
			IOMMUGroup:              group,
			IOMMUMode:               mode,
			NUMANode:                readInt(filepath.Join(devicePath, "numa_node"), -1),
			DeviceNodes:             nodes,
		}
		name := dpdkDeviceName(address)
		devices[name] = dpdkDevice{Name: name, State: state}
	}
	return devices, nil
}

func deviceNodes(cfg config.DPDKConfig, devicePath, driver, group string) (string, []string) {
	switch driver {
	case "vfio-pci":
		controlCheck := filepath.Join(cfg.DevPath, "vfio/vfio")
		control := filepath.Join(cfg.CDIHostDevPath, "vfio/vfio")
		if !fileExists(controlCheck) || group == "" {
			return "", nil
		}
		normalCheck := filepath.Join(cfg.DevPath, "vfio", group)
		normal := filepath.Join(cfg.CDIHostDevPath, "vfio", group)
		if fileExists(normalCheck) {
			return "iommu", []string{control, normal}
		}
		unsafeCheck := filepath.Join(cfg.DevPath, "vfio", "noiommu-"+group)
		unsafe := filepath.Join(cfg.CDIHostDevPath, "vfio", "noiommu-"+group)
		if fileExists(unsafeCheck) {
			return "noiommu", []string{control, unsafe}
		}
	case "uio_pci_generic", "igb_uio":
		entries, _ := filepath.Glob(filepath.Join(devicePath, "uio/uio*"))
		if len(entries) == 1 && fileExists(filepath.Join(cfg.DevPath, filepath.Base(entries[0]))) {
			return "uio", []string{filepath.Join(cfg.CDIHostDevPath, filepath.Base(entries[0]))}
		}
	}
	return "", nil
}

func selectorAllows(selector config.PCISelector, address, vendor, device string, emptyMatches bool) bool {
	configured := len(selector.PCIAddresses)+len(selector.Vendors)+len(selector.Devices) > 0
	if !configured {
		return emptyMatches
	}
	return (len(selector.PCIAddresses) == 0 || slices.Contains(selector.PCIAddresses, address)) &&
		(len(selector.Vendors) == 0 || slices.Contains(selector.Vendors, vendor)) &&
		(len(selector.Devices) == 0 || slices.Contains(selector.Devices, device))
}

func classAllowed(class string, classes []string) bool {
	for _, allowed := range classes {
		if strings.HasPrefix(class, allowed) {
			return true
		}
	}
	return false
}

func compatibleKernelDrivers(cfg config.DPDKConfig, devicePath, vendor, device, current string) []string {
	key := vendor + ":" + device
	if override := cfg.CompatibleDriverOverrides[key]; len(override) > 0 {
		return uniqueSorted(override, current)
	}
	modalias := strings.TrimSpace(readFile(filepath.Join(devicePath, "modalias")))
	if modalias == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(cfg.ModulesPath, "*", "modules.alias"))
	matched := []string{}
	for _, aliasFile := range files {
		file, err := os.Open(aliasFile)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 3 || fields[0] != "alias" {
				continue
			}
			ok, _ := path.Match(strings.ToLower(fields[1]), strings.ToLower(modalias))
			if ok {
				matched = append(matched, strings.ReplaceAll(fields[2], "_", "-"))
			}
		}
		_ = file.Close()
	}
	return uniqueSorted(matched, current)
}

func uniqueSorted(values []string, excluded string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == strings.ToLower(excluded) || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func dpdkDeviceAttributes(device dpdkDevice) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{}
	setBoolAttribute(attrs, AttrDPDK, true)
	setBoolAttribute(attrs, AttrMacvlan, false)
	setBoolAttribute(attrs, AttrIPvlan, false)
	setBoolAttribute(attrs, AttrHostDevice, false)
	setStringAttribute(attrs, AttrTypes, "dpdk")
	setStringAttribute(attrs, AttrPolicy, "exclusive")
	setStringAttribute(attrs, AttrBusType, "pci")
	setStringAttribute(attrs, AttrPCIAddress, device.State.PCIAddress)
	setStringAttribute(attrs, AttrPCIClass, device.State.PCIClass)
	setStringAttribute(attrs, AttrPCIVendorID, device.State.VendorID)
	setStringAttribute(attrs, AttrPCIDeviceID, device.State.DeviceID)
	setStringAttribute(attrs, AttrVendorName, attributeString(device.State.VendorName))
	setStringAttribute(attrs, AttrDeviceName, attributeString(device.State.DeviceName))
	setStringAttribute(attrs, AttrSubsystemVendorID, device.State.SubsystemVendorID)
	setStringAttribute(attrs, AttrSubsystemDeviceID, device.State.SubsystemDeviceID)
	setStringAttribute(attrs, AttrKernelDriver, device.State.CurrentDriver)
	setStringAttribute(attrs, AttrCompatibleDriver, strings.Join(device.State.CompatibleKernelDrivers, ","))
	setStringAttribute(attrs, AttrIOMMUGroup, device.State.IOMMUGroup)
	setStringAttribute(attrs, AttrIOMMUMode, device.State.IOMMUMode)
	setStringAttribute(attrs, AttrNUMANode, strconv.Itoa(device.State.NUMANode))
	return attrs
}

func writeCDISpec(cfg config.DPDKConfig, driverName, claimUID, deviceName string, state DPDKDeviceState) (string, string, error) {
	uniqueName := strings.ToLower(strings.ReplaceAll(claimUID+"-"+deviceName, "_", "-"))
	kind := driverName + "/dpdk"
	deviceID := kind + "=" + uniqueName
	type deviceNode struct {
		HostPath    string `json:"hostPath"`
		Path        string `json:"path"`
		Permissions string `json:"permissions"`
	}
	nodes := make([]deviceNode, 0, len(state.DeviceNodes))
	for _, hostPath := range state.DeviceNodes {
		containerPath := hostPath
		if state.IOMMUMode == "noiommu" && strings.Contains(hostPath, "/vfio/noiommu-") {
			containerPath = path.Join(path.Dir(hostPath), state.IOMMUGroup)
		}
		nodes = append(nodes, deviceNode{HostPath: hostPath, Path: containerPath, Permissions: "rw"})
	}
	spec := struct {
		CDIVersion string `json:"cdiVersion"`
		Kind       string `json:"kind"`
		Devices    []struct {
			Name           string `json:"name"`
			ContainerEdits struct {
				DeviceNodes []deviceNode `json:"deviceNodes"`
				Env         []string     `json:"env"`
			} `json:"containerEdits"`
		} `json:"devices"`
	}{CDIVersion: "0.6.0", Kind: kind}
	spec.Devices = append(spec.Devices, struct {
		Name           string `json:"name"`
		ContainerEdits struct {
			DeviceNodes []deviceNode `json:"deviceNodes"`
			Env         []string     `json:"env"`
		} `json:"containerEdits"`
	}{Name: uniqueName})
	spec.Devices[0].ContainerEdits.DeviceNodes = nodes
	spec.Devices[0].ContainerEdits.Env = []string{
		"LINUX_NET_DRA_PCI_ADDRESS=" + state.PCIAddress,
		"LINUX_NET_DRA_IOMMU_GROUP=" + state.IOMMUGroup,
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(cfg.CDIPath, 0755); err != nil {
		return "", "", err
	}
	finalPath := filepath.Join(cfg.CDIPath, "linux-net-dra-"+uniqueName+".json")
	tmp, err := os.CreateTemp(cfg.CDIPath, ".linux-net-dra-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Windows does not replace an existing file. The Linux production path
		// remains a single atomic rename.
		if removeErr := os.Remove(finalPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return "", "", err
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return "", "", err
		}
	}
	return deviceID, finalPath, nil
}

func dpdkPreparedMessage(cfg DeviceConfig) string {
	if cfg.DPDK == nil {
		return "DPDK device prepared"
	}
	return fmt.Sprintf("DPDK device %s prepared through CDI, driver %s, IOMMU mode %s, group %s", cfg.DPDK.PCIAddress, cfg.DPDK.CurrentDriver, cfg.DPDK.IOMMUMode, cfg.DPDK.IOMMUGroup)
}

func dpdkInjectedMessage(cfg DeviceConfig) string {
	if cfg.DPDK == nil {
		return "DPDK device injected"
	}
	return fmt.Sprintf("DPDK device %s injected through CDI, driver %s, IOMMU mode %s, group %s", cfg.DPDK.PCIAddress, cfg.DPDK.CurrentDriver, cfg.DPDK.IOMMUMode, cfg.DPDK.IOMMUGroup)
}

func removeClaimCDISpecs(cdiPath, claimUID string) error {
	paths, err := filepath.Glob(filepath.Join(cdiPath, "linux-net-dra-"+strings.ToLower(claimUID)+"-*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func loadPCIIDs(filePath string) map[string]string {
	result := map[string]string{}
	file, err := os.Open(filePath)
	if err != nil {
		return result
	}
	defer file.Close()
	vendor := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "C ") {
			continue
		}
		if !strings.HasPrefix(line, "\t") && len(line) > 6 {
			vendor = strings.ToLower(strings.TrimSpace(line[:4]))
			result["v:"+vendor] = strings.TrimSpace(line[4:])
			continue
		}
		if strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t") && vendor != "" {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				result["d:"+vendor+":"+strings.ToLower(fields[0])] = strings.Join(fields[1:], " ")
			}
		}
	}
	return result
}

func attributeString(value string) string {
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

func symlinkBase(filePath string) string {
	target, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func dpdkDeviceName(address string) string {
	replacer := strings.NewReplacer(":", "-", ".", "-")
	return "pci-" + replacer.Replace(strings.ToLower(address))
}

func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

func readFile(filePath string) string {
	data, _ := os.ReadFile(filePath)
	return string(data)
}

func readInt(filePath string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(readFile(filePath)))
	if err != nil {
		return fallback
	}
	return value
}
