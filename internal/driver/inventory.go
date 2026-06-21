package driver

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vishvananda/netlink"
)

var (
	pciAddressPattern = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)
	pcieRootPattern   = regexp.MustCompile(`^pci[0-9a-fA-F]{4}:[0-9a-fA-F]{2}$`)
)

func interfaceIdentity(name string) InterfaceIdentity {
	return interfaceIdentityAt("/sys/class/net", name)
}

func interfaceIdentityAt(sysClassNet, name string) InterfaceIdentity {
	identity := InterfaceIdentity{}
	devicePath := filepath.Join(sysClassNet, name, "device")
	if target, err := filepath.EvalSymlinks(devicePath); err == nil {
		identity.PCIAddress, identity.PCIeRoot = pciTopologyFromPath(target)
		base := filepath.Base(target)
		switch {
		case pciAddressPattern.MatchString(base):
			identity.BusType = "pci"
		case strings.HasPrefix(base, "virtio"):
			identity.BusType = "virtio"
		default:
			identity.BusType = "platform"
		}
	}
	if target, err := filepath.EvalSymlinks(filepath.Join(devicePath, "driver")); err == nil {
		identity.KernelDriver = filepath.Base(target)
	}
	values := readUevent(filepath.Join(devicePath, "uevent"))
	if values["DRIVER"] != "" {
		identity.KernelDriver = values["DRIVER"]
	}
	if values["PCI_SLOT_NAME"] != "" {
		identity.BusType = "pci"
		identity.PCIAddress = values["PCI_SLOT_NAME"]
	}
	if pciID := strings.Split(values["PCI_ID"], ":"); len(pciID) == 2 {
		identity.PCIVendorID = strings.ToLower(pciID[0])
		identity.PCIDeviceID = strings.ToLower(pciID[1])
	}
	if identity.PCIVendorID == "" {
		identity.PCIVendorID = trimHexFile(filepath.Join(devicePath, "vendor"))
	}
	if identity.PCIDeviceID == "" {
		identity.PCIDeviceID = trimHexFile(filepath.Join(devicePath, "device"))
	}
	return identity
}

func pciTopologyAt(sysfsPath, pciAddress string) (string, string) {
	target, err := filepath.EvalSymlinks(filepath.Join(sysfsPath, "bus", "pci", "devices", pciAddress))
	if err != nil {
		return strings.ToLower(pciAddress), ""
	}
	address, root := pciTopologyFromPath(target)
	if address == "" {
		address = strings.ToLower(pciAddress)
	}
	return address, root
}

func pciTopologyFromPath(path string) (string, string) {
	var address, root string
	for _, component := range strings.FieldsFunc(filepath.Clean(path), func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		switch {
		case pcieRootPattern.MatchString(component) && root == "":
			root = strings.ToLower(component)
		case pciAddressPattern.MatchString(component):
			address = strings.ToLower(component)
		}
	}
	return address, root
}

func readUevent(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func trimHexFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(string(data))), "0x")
}

func snapshotHostDevice(link netlink.Link, podInterfaceName string) (*HostDeviceState, error) {
	addresses, err := netlink.AddrList(link, 0)
	if err != nil {
		return nil, fmt.Errorf("list addresses on %s: %w", link.Attrs().Name, err)
	}
	cidrs := make([]string, 0, len(addresses))
	addressState := make([]HostAddressState, 0, len(addresses))
	for _, address := range addresses {
		if address.IPNet != nil {
			cidrs = append(cidrs, address.IPNet.String())
			state := HostAddressState{
				CIDR:         address.IPNet.String(),
				Label:        address.Label,
				Flags:        address.Flags,
				Scope:        address.Scope,
				PreferredLft: address.PreferedLft,
				ValidLft:     address.ValidLft,
			}
			if address.Peer != nil {
				state.Peer = address.Peer.String()
			}
			if address.Broadcast != nil {
				state.Broadcast = address.Broadcast.String()
			}
			addressState = append(addressState, state)
		}
	}
	return &HostDeviceState{
		OriginalName:      link.Attrs().Name,
		PodInterfaceName:  podInterfaceName,
		OriginalAdminUp:   link.Attrs().Flags&net.FlagUp != 0,
		OriginalOperState: strings.ToLower(link.Attrs().OperState.String()),
		OriginalMTU:       link.Attrs().MTU,
		OriginalMAC:       link.Attrs().HardwareAddr.String(),
		OriginalAddresses: cidrs,
		AddressState:      addressState,
	}, nil
}
