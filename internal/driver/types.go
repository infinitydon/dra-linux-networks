package driver

import "k8s.io/apimachinery/pkg/types"

const (
	AttrPrefix       = "linux-net.dra.infinitydon.com"
	AttrInterface    = AttrPrefix + "/interface"
	AttrMAC          = AttrPrefix + "/mac"
	AttrMTU          = AttrPrefix + "/mtu"
	AttrDefault      = AttrPrefix + "/default"
	AttrTypes        = AttrPrefix + "/types"
	AttrMacvlanModes = AttrPrefix + "/macvlanModes"
	AttrIPvlanModes  = AttrPrefix + "/ipvlanModes"
)

type NetworkConfig struct {
	Type          string   `json:"type,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	InterfaceName string   `json:"interfaceName,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	Addresses     []string `json:"addresses,omitempty"`
	Gateway       string   `json:"gateway,omitempty"`
	Routes        []Route  `json:"routes,omitempty"`
}

type Route struct {
	Destination string `json:"destination,omitempty"`
	Gateway     string `json:"gateway,omitempty"`
}

type DeviceConfig struct {
	Claim      types.NamespacedName `json:"claim"`
	ClaimUID   types.UID            `json:"claimUID"`
	DeviceName string               `json:"deviceName"`
	ParentName string               `json:"parentName"`
	Network    NetworkConfig        `json:"network"`
	AttachedIf string               `json:"attachedInterface,omitempty"`
}

type PodConfig struct {
	PodUID  types.UID               `json:"podUID"`
	NetNS   string                  `json:"netns,omitempty"`
	Devices map[string]DeviceConfig `json:"devices"`
}
