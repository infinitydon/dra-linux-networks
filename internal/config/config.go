package config

import (
	"encoding/json"
	"fmt"
	"os"
)

const DefaultDriverName = "linux-net.dra.infinitydon.com"

type Config struct {
	DriverName string            `json:"driverName"`
	Interfaces []InterfaceConfig `json:"interfaces"`
	IPPools    []IPPool          `json:"ipPools"`
}

type InterfaceConfig struct {
	Name             string   `json:"name"`
	Default          bool     `json:"default"`
	AllocationPolicy string   `json:"allocationPolicy"`
	Types            []string `json:"types"`
	MacvlanModes     []string `json:"macvlanModes"`
	IPvlanModes      []string `json:"ipvlanModes"`
	DefaultType      string   `json:"defaultType"`
	DefaultMode      string   `json:"defaultMode"`
	DefaultPodName   string   `json:"defaultPodInterfaceName"`
	MTU              int      `json:"mtu"`
	AllowUnsafe      bool     `json:"allowUnsafe"`
}

type IPPool struct {
	Name         string    `json:"name"`
	Subnet       string    `json:"subnet"`
	RangeStart   string    `json:"rangeStart"`
	RangeEnd     string    `json:"rangeEnd"`
	Allocations  []IPRange `json:"allocations"`
	Reservations []IPRange `json:"reservations"`
	Gateway      string    `json:"gateway"`
	Routes       []Route   `json:"routes"`
}

type IPRange struct {
	Name       string   `json:"name,omitempty"`
	Addresses  []string `json:"addresses,omitempty"`
	RangeStart string   `json:"rangeStart,omitempty"`
	RangeEnd   string   `json:"rangeEnd,omitempty"`
}

type Route struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.DriverName == "" {
		cfg.DriverName = DefaultDriverName
	}
	if len(cfg.Interfaces) == 0 {
		return nil, fmt.Errorf("at least one interface must be configured")
	}
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "" {
			return nil, fmt.Errorf("interfaces[%d].name is required", i)
		}
		if len(cfg.Interfaces[i].Types) == 0 {
			cfg.Interfaces[i].Types = []string{"macvlan", "ipvlan"}
		}
		if cfg.Interfaces[i].AllocationPolicy == "" {
			cfg.Interfaces[i].AllocationPolicy = "shared"
		}
		if cfg.Interfaces[i].AllocationPolicy != "shared" && cfg.Interfaces[i].AllocationPolicy != "exclusive" {
			return nil, fmt.Errorf("interfaces[%d].allocationPolicy must be shared or exclusive", i)
		}
		hostDevice := false
		sharedType := false
		for _, networkType := range cfg.Interfaces[i].Types {
			switch networkType {
			case "host-device":
				hostDevice = true
			case "macvlan", "ipvlan":
				sharedType = true
			default:
				return nil, fmt.Errorf("interfaces[%d].types contains unsupported type %q", i, networkType)
			}
		}
		if hostDevice && (cfg.Interfaces[i].AllocationPolicy != "exclusive" || sharedType) {
			return nil, fmt.Errorf("interfaces[%d] host-device must be exclusive and cannot be combined with shared link types", i)
		}
		if !hostDevice && cfg.Interfaces[i].AllocationPolicy != "shared" {
			return nil, fmt.Errorf("interfaces[%d] exclusive policy requires host-device type", i)
		}
		if len(cfg.Interfaces[i].MacvlanModes) == 0 {
			cfg.Interfaces[i].MacvlanModes = []string{"bridge", "private", "vepa", "passthru"}
		}
		if len(cfg.Interfaces[i].IPvlanModes) == 0 {
			cfg.Interfaces[i].IPvlanModes = []string{"l2", "l3", "l3s"}
		}
		if cfg.Interfaces[i].DefaultType == "" {
			cfg.Interfaces[i].DefaultType = cfg.Interfaces[i].Types[0]
		}
		if cfg.Interfaces[i].DefaultMode == "" && cfg.Interfaces[i].DefaultType != "host-device" {
			cfg.Interfaces[i].DefaultMode = "bridge"
		}
		if cfg.Interfaces[i].DefaultPodName == "" && cfg.Interfaces[i].DefaultType != "host-device" {
			cfg.Interfaces[i].DefaultPodName = "net1"
		}
	}
	for i := range cfg.IPPools {
		if cfg.IPPools[i].Name == "" {
			return nil, fmt.Errorf("ipPools[%d].name is required", i)
		}
		if cfg.IPPools[i].Subnet == "" {
			return nil, fmt.Errorf("ipPools[%d].subnet is required", i)
		}
		if len(cfg.IPPools[i].Allocations) == 0 && cfg.IPPools[i].RangeStart != "" && cfg.IPPools[i].RangeEnd != "" {
			cfg.IPPools[i].Allocations = []IPRange{{
				RangeStart: cfg.IPPools[i].RangeStart,
				RangeEnd:   cfg.IPPools[i].RangeEnd,
			}}
		}
		if len(cfg.IPPools[i].Allocations) == 0 {
			return nil, fmt.Errorf("ipPools[%d].allocations is required", i)
		}
	}
	return cfg, nil
}
