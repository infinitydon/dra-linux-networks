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
	Name           string   `json:"name"`
	Default        bool     `json:"default"`
	Types          []string `json:"types"`
	MacvlanModes   []string `json:"macvlanModes"`
	IPvlanModes    []string `json:"ipvlanModes"`
	DefaultType    string   `json:"defaultType"`
	DefaultMode    string   `json:"defaultMode"`
	DefaultPodName string   `json:"defaultPodInterfaceName"`
	MTU            int      `json:"mtu"`
}

type IPPool struct {
	Name       string  `json:"name"`
	Subnet     string  `json:"subnet"`
	RangeStart string  `json:"rangeStart"`
	RangeEnd   string  `json:"rangeEnd"`
	Gateway    string  `json:"gateway"`
	Routes     []Route `json:"routes"`
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
		if len(cfg.Interfaces[i].MacvlanModes) == 0 {
			cfg.Interfaces[i].MacvlanModes = []string{"bridge", "private", "vepa", "passthru"}
		}
		if len(cfg.Interfaces[i].IPvlanModes) == 0 {
			cfg.Interfaces[i].IPvlanModes = []string{"l2", "l3", "l3s"}
		}
		if cfg.Interfaces[i].DefaultType == "" {
			cfg.Interfaces[i].DefaultType = "macvlan"
		}
		if cfg.Interfaces[i].DefaultMode == "" {
			cfg.Interfaces[i].DefaultMode = "bridge"
		}
		if cfg.Interfaces[i].DefaultPodName == "" {
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
		if cfg.IPPools[i].RangeStart == "" {
			return nil, fmt.Errorf("ipPools[%d].rangeStart is required", i)
		}
		if cfg.IPPools[i].RangeEnd == "" {
			return nil, fmt.Errorf("ipPools[%d].rangeEnd is required", i)
		}
	}
	return cfg, nil
}
