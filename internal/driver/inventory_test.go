package driver

import (
	"os"
	"path/filepath"
	"testing"

	resourceapi "k8s.io/api/resource/v1"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

func TestInterfaceIdentityFromSysfs(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "enp8s21", "device")
	if err := os.MkdirAll(device, 0755); err != nil {
		t.Fatal(err)
	}
	uevent := "DRIVER=e1000\nPCI_ID=8086:100E\nPCI_SLOT_NAME=0000:00:15.0\n"
	if err := os.WriteFile(filepath.Join(device, "uevent"), []byte(uevent), 0600); err != nil {
		t.Fatal(err)
	}
	identity := interfaceIdentityAt(root, "enp8s21")
	if identity.KernelDriver != "e1000" || identity.BusType != "pci" || identity.PCIAddress != "0000:00:15.0" || identity.PCIVendorID != "8086" || identity.PCIDeviceID != "100e" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestPCITopologyFromResolvedSysfsPath(t *testing.T) {
	for _, test := range []struct {
		name        string
		path        string
		wantAddress string
		wantRoot    string
	}{
		{
			name:        "virtio child",
			path:        "/sys/devices/pci0000:00/0000:00:1e.0/0000:07:01.0/0000:08:14.0/virtio5",
			wantAddress: "0000:08:14.0",
			wantRoot:    "pci0000:00",
		},
		{
			name:        "different root",
			path:        "/sys/devices/pci0001:80/0001:80:02.0/0001:81:00.0",
			wantAddress: "0001:81:00.0",
			wantRoot:    "pci0001:80",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			address, root := pciTopologyFromPath(test.path)
			if address != test.wantAddress || root != test.wantRoot {
				t.Fatalf("topology = (%q, %q), want (%q, %q)", address, root, test.wantAddress, test.wantRoot)
			}
		})
	}
}

func TestExclusiveHostDeviceAttributes(t *testing.T) {
	ifc := config.InterfaceConfig{Name: "enp8s21", AllocationPolicy: "exclusive", Types: []string{"host-device"}}
	attrs := deviceAttributesFromValues(ifc, "02:00:00:00:00:01", 1500, InterfaceIdentity{KernelDriver: "e1000", PCIAddress: "0000:00:15.0", PCIeRoot: "pci0000:00"})
	assertStringAttribute(t, attrs, AttrPolicy, "exclusive")
	assertStringAttribute(t, attrs, AttrKernelDriver, "e1000")
	assertStringAttribute(t, attrs, AttrPCIAddress, "0000:00:15.0")
	assertStringAttribute(t, attrs, AttrStandardPCIBusID, "0000:00:15.0")
	assertStringAttribute(t, attrs, AttrStandardPCIeRoot, "pci0000:00")
	if value := attrs[resourceapi.QualifiedName(AttrHostDevice)].BoolValue; value == nil || !*value {
		t.Fatal("hostDevice attribute is not true")
	}
}

func assertStringAttribute(t *testing.T, attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, name, want string) {
	t.Helper()
	value := attrs[resourceapi.QualifiedName(name)].StringValue
	if value == nil || *value != want {
		t.Fatalf("attribute %s = %v, want %q", name, value, want)
	}
}
