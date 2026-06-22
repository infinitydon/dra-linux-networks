package driver

import "k8s.io/apimachinery/pkg/types"

const (
	AttrPrefix            = "linux-net.dra.infinitydon.com"
	AttrInterface         = AttrPrefix + "/interface"
	AttrMAC               = AttrPrefix + "/mac"
	AttrMTU               = AttrPrefix + "/mtu"
	AttrDefault           = AttrPrefix + "/default"
	AttrMacvlan           = AttrPrefix + "/macvlan"
	AttrIPvlan            = AttrPrefix + "/ipvlan"
	AttrTypes             = AttrPrefix + "/types"
	AttrMacvlanModes      = AttrPrefix + "/macvlanModes"
	AttrIPvlanModes       = AttrPrefix + "/ipvlanModes"
	AttrHostDevice        = AttrPrefix + "/hostDevice"
	AttrKernelDriver      = AttrPrefix + "/kernelDriver"
	AttrBusType           = AttrPrefix + "/busType"
	AttrPCIAddress        = AttrPrefix + "/pciAddress"
	AttrPCIVendorID       = AttrPrefix + "/pciVendorID"
	AttrPCIDeviceID       = AttrPrefix + "/pciDeviceID"
	AttrAdminState        = AttrPrefix + "/adminState"
	AttrOperState         = AttrPrefix + "/operState"
	AttrPolicy            = AttrPrefix + "/allocationPolicy"
	AttrDPDK              = AttrPrefix + "/dpdk"
	AttrPCIClass          = AttrPrefix + "/pciClass"
	AttrVendorName        = AttrPrefix + "/vendorName"
	AttrDeviceName        = AttrPrefix + "/deviceName"
	AttrSubsystemVendorID = AttrPrefix + "/subsystemVendorID"
	AttrSubsystemDeviceID = AttrPrefix + "/subsystemDeviceID"
	AttrCompatibleDriver  = AttrPrefix + "/compatibleKernelDriver"
	AttrIOMMUGroup        = AttrPrefix + "/iommuGroup"
	AttrIOMMUMode         = AttrPrefix + "/iommuMode"
	AttrNUMANode          = AttrPrefix + "/numaNode"
	AttrStandardPCIBusID  = "resource.kubernetes.io/pciBusID"
	AttrStandardPCIeRoot  = "resource.kubernetes.io/pcieRoot"
	PodClaimNameAnnotation = "resource.kubernetes.io/pod-claim-name"
)

type InterfaceIdentity struct {
	KernelDriver string `json:"kernelDriver,omitempty"`
	BusType      string `json:"busType,omitempty"`
	PCIAddress   string `json:"pciAddress,omitempty"`
	PCIeRoot     string `json:"pcieRoot,omitempty"`
	PCIVendorID  string `json:"pciVendorID,omitempty"`
	PCIDeviceID  string `json:"pciDeviceID,omitempty"`
}

type DPDKDeviceState struct {
	PCIAddress              string   `json:"pciAddress"`
	PCIeRoot                string   `json:"pcieRoot,omitempty"`
	PCIClass                string   `json:"pciClass,omitempty"`
	VendorID                string   `json:"vendorID,omitempty"`
	VendorName              string   `json:"vendorName,omitempty"`
	DeviceID                string   `json:"deviceID,omitempty"`
	DeviceName              string   `json:"deviceName,omitempty"`
	SubsystemVendorID       string   `json:"subsystemVendorID,omitempty"`
	SubsystemDeviceID       string   `json:"subsystemDeviceID,omitempty"`
	CurrentDriver           string   `json:"currentDriver"`
	CompatibleKernelDrivers []string `json:"compatibleKernelDrivers,omitempty"`
	IOMMUGroup              string   `json:"iommuGroup,omitempty"`
	IOMMUMode               string   `json:"iommuMode"`
	NUMANode                int      `json:"numaNode"`
	DeviceNodes             []string `json:"deviceNodes"`
	CDIDeviceID             string   `json:"cdiDeviceID,omitempty"`
	CDISpecPath             string   `json:"cdiSpecPath,omitempty"`
}

type HostDeviceState struct {
	OriginalName      string             `json:"originalName"`
	PodInterfaceName  string             `json:"podInterfaceName"`
	OriginalAdminUp   bool               `json:"originalAdminUp"`
	OriginalOperState string             `json:"originalOperState,omitempty"`
	OriginalMTU       int                `json:"originalMTU"`
	OriginalMAC       string             `json:"originalMAC,omitempty"`
	OriginalAddresses []string           `json:"originalAddresses,omitempty"`
	AddressState      []HostAddressState `json:"addressState,omitempty"`
	Restored          bool               `json:"restored,omitempty"`
}

type HostAddressState struct {
	CIDR         string `json:"cidr"`
	Label        string `json:"label,omitempty"`
	Flags        int    `json:"flags,omitempty"`
	Scope        int    `json:"scope,omitempty"`
	Peer         string `json:"peer,omitempty"`
	Broadcast    string `json:"broadcast,omitempty"`
	PreferredLft int    `json:"preferredLifetime,omitempty"`
	ValidLft     int    `json:"validLifetime,omitempty"`
}

type NetworkConfig struct {
	Type          string   `json:"type,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	InterfaceName string   `json:"interfaceName,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	IPPool        string   `json:"ipPool,omitempty"`
	Address       string   `json:"address,omitempty"`
	Addresses     []string `json:"addresses,omitempty"`
	Gateway       string   `json:"gateway,omitempty"`
	Routes        []Route  `json:"routes,omitempty"`
}

type Route struct {
	Destination string `json:"destination,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
}

type DeviceConfig struct {
	Claim            types.NamespacedName `json:"claim"`
	ClaimUID         types.UID            `json:"claimUID"`
	DriverName       string               `json:"driverName"`
	PoolName         string               `json:"poolName"`
	DeviceName       string               `json:"deviceName"`
	ShareID          *string              `json:"shareID,omitempty"`
	ParentName       string               `json:"parentName"`
	AllocationPolicy string               `json:"allocationPolicy"`
	Identity         InterfaceIdentity    `json:"identity,omitempty"`
	HostDevice       *HostDeviceState     `json:"hostDevice,omitempty"`
	DPDK             *DPDKDeviceState     `json:"dpdk,omitempty"`
	Network          NetworkConfig        `json:"network"`
	AttachedIf       string               `json:"attachedInterface,omitempty"`
	HardwareAddress  string               `json:"hardwareAddress,omitempty"`
	LifecycleState   string               `json:"lifecycleState,omitempty"`
}

type PodConfig struct {
	PodUID  types.UID               `json:"podUID"`
	NetNS   string                  `json:"netns,omitempty"`
	Devices map[string]DeviceConfig `json:"devices"`
}

type IPAllocation struct {
	Pool     string    `json:"pool"`
	Address  string    `json:"address"`
	ClaimUID types.UID `json:"claimUID"`
	PodUID   types.UID `json:"podUID"`
}
