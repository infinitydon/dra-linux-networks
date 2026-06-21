package driver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/infinitydon/dra-linux-networks/internal/config"
)

func TestDPDKSelectorUsesANDWithinSelector(t *testing.T) {
	selector := config.PCISelector{
		Vendors:      []string{"8086"},
		Devices:      []string{"154c"},
		PCIAddresses: []string{"0000:01:00.0"},
	}
	if !selectorAllows(selector, "0000:01:00.0", "8086", "154c", true) {
		t.Fatal("matching device was rejected")
	}
	if selectorAllows(selector, "0000:02:00.0", "8086", "154c", true) {
		t.Fatal("selector matched a different PCI address")
	}
}

func TestValidateDPDKConfigRejectsIPAM(t *testing.T) {
	err := validateDPDKConfig(NetworkConfig{Type: "dpdk", IPPool: "lan-88"})
	if err == nil || !strings.Contains(err.Error(), "do not support") {
		t.Fatalf("error = %v, want DPDK IPAM rejection", err)
	}
	if err := validateDPDKConfig(NetworkConfig{Type: "dpdk"}); err != nil {
		t.Fatalf("minimal DPDK config: %v", err)
	}
}

func TestWriteCDISpecMapsNoIOMMUNode(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DPDKConfig{CDIPath: dir}
	state := DPDKDeviceState{
		PCIAddress:  "0000:01:00.0",
		IOMMUGroup:  "4",
		IOMMUMode:   "noiommu",
		DeviceNodes: []string{"/dev/vfio/vfio", "/dev/vfio/noiommu-4"},
	}
	id, specPath, err := writeCDISpec(cfg, "linux-net.dra.example.com", "claim-uid", "pci-0000-01-00-0", state)
	if err != nil {
		t.Fatal(err)
	}
	if id != "linux-net.dra.example.com/dpdk=claim-uid-pci-0000-01-00-0" {
		t.Fatalf("CDI ID = %q", id)
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Devices []struct {
			ContainerEdits struct {
				DeviceNodes []struct {
					HostPath string `json:"hostPath"`
					Path     string `json:"path"`
				} `json:"deviceNodes"`
			} `json:"containerEdits"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	got := spec.Devices[0].ContainerEdits.DeviceNodes[1]
	if got.HostPath != "/dev/vfio/noiommu-4" || got.Path != "/dev/vfio/4" {
		t.Fatalf("no-IOMMU mapping = %+v", got)
	}
	if err := removeClaimCDISpecs(dir, "claim-uid"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Clean(specPath)); !os.IsNotExist(err) {
		t.Fatalf("CDI spec still exists after cleanup: %v", err)
	}
	if err := removeClaimCDISpecs(dir, "claim-uid"); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
}
