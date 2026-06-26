package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterfaceAllocationPolicyValidation(t *testing.T) {
	tests := []struct {
		name          string
		interfaceJSON string
		wantError     string
	}{
		{name: "shared macvlan", interfaceJSON: `{"name":"eth1","allocationPolicy":"shared","types":["macvlan"]}`},
		{name: "exclusive host device", interfaceJSON: `{"name":"eth2","allocationPolicy":"exclusive","types":["host-device"]}`},
		{name: "host device cannot be shared", interfaceJSON: `{"name":"eth2","allocationPolicy":"shared","types":["host-device"]}`, wantError: "must be exclusive"},
		{name: "host device cannot mix", interfaceJSON: `{"name":"eth2","allocationPolicy":"exclusive","types":["host-device","macvlan"]}`, wantError: "cannot be combined"},
		{name: "exclusive requires host device", interfaceJSON: `{"name":"eth2","allocationPolicy":"exclusive","types":["ipvlan"]}`, wantError: "requires host-device"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := `{"interfaces":[` + test.interfaceJSON + `]}`
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Interfaces[0].DefaultType == "" {
				t.Fatal("default type was not populated")
			}
		})
	}
}

func TestEmptyDeviceInventoryIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load empty inventory: %v", err)
	}
	if len(cfg.Interfaces) != 0 || cfg.DPDK.Enabled {
		t.Fatalf("unexpected inventory: %+v", cfg)
	}
}

func TestDPDKPCIClassDefaultDistinguishesOmittedAndEmpty(t *testing.T) {
	tests := []struct {
		name        string
		dpdkJSON    string
		wantClasses []string
	}{
		{name: "omitted defaults to ethernet", dpdkJSON: `{"enabled":true}`, wantClasses: []string{"0200"}},
		{name: "explicit empty disables filtering", dpdkJSON: `{"enabled":true,"pciClasses":[]}`, wantClasses: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"dpdk":`+test.dpdkJSON+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(cfg.DPDK.PCIClasses, ",") != strings.Join(test.wantClasses, ",") {
				t.Fatalf("pciClasses = %v, want %v", cfg.DPDK.PCIClasses, test.wantClasses)
			}
		})
	}
}

func TestDPDKAllowedKernelDriversSupportsLegacyAlias(t *testing.T) {
	tests := []struct {
		name    string
		dpdk    string
		want    string
		legacy  string
	}{
		{name: "default", dpdk: `{"enabled":true}`, want: "vfio-pci", legacy: "vfio-pci"},
		{name: "allowed kernel drivers", dpdk: `{"enabled":true,"allowedKernelDrivers":["VFIO-PCI","uio_pci_generic"]}`, want: "vfio-pci,uio_pci_generic", legacy: "vfio-pci,uio_pci_generic"},
		{name: "legacy drivers alias", dpdk: `{"enabled":true,"drivers":["igb_uio"]}`, want: "igb_uio", legacy: "igb_uio"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"dpdk":`+test.dpdk+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(cfg.DPDK.AllowedKernelDrivers, ","); got != test.want {
				t.Fatalf("allowedKernelDrivers = %q, want %q", got, test.want)
			}
			if got := strings.Join(cfg.DPDK.Drivers, ","); got != test.legacy {
				t.Fatalf("drivers alias = %q, want %q", got, test.legacy)
			}
		})
	}
}
