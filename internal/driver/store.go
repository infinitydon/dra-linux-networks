package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

type Store struct {
	path string
	mu   sync.Mutex
	State
}

type State struct {
	Pods          map[types.UID]PodConfig    `json:"pods"`
	IPAllocations map[types.UID]IPAllocation `json:"ipAllocations"`
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path: path,
		State: State{
			Pods:          map[types.UID]PodConfig{},
			IPAllocations: map[types.UID]IPAllocation{},
		},
	}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.State); err != nil {
			legacy := map[types.UID]PodConfig{}
			if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
				return nil, err
			}
			s.Pods = legacy
		}
		if s.Pods == nil {
			legacy := map[types.UID]PodConfig{}
			if legacyErr := json.Unmarshal(data, &legacy); legacyErr == nil && len(legacy) > 0 {
				s.Pods = legacy
			}
		}
	}
	if s.Pods == nil {
		s.Pods = map[types.UID]PodConfig{}
	}
	if s.IPAllocations == nil {
		s.IPAllocations = map[types.UID]IPAllocation{}
	}
	if s.migrateDeviceKeys() && s.path != "" {
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func deviceStoreKey(claimUID types.UID, deviceName string, shareID *string) string {
	key := fmt.Sprintf("%s/%s", claimUID, deviceName)
	if shareID != nil && *shareID != "" {
		key += "/" + *shareID
	}
	return key
}

func (s *Store) migrateDeviceKeys() bool {
	changed := false
	for podUID, pod := range s.Pods {
		devices := make(map[string]DeviceConfig, len(pod.Devices))
		for oldKey, cfg := range pod.Devices {
			key := oldKey
			if cfg.ClaimUID != "" && cfg.DeviceName != "" {
				key = deviceStoreKey(cfg.ClaimUID, cfg.DeviceName, cfg.ShareID)
			}
			devices[key] = cfg
			changed = changed || key != oldKey
		}
		pod.Devices = devices
		s.Pods[podUID] = pod
	}
	return changed
}

func (s *Store) SetDevice(podUID types.UID, deviceName string, cfg DeviceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod := s.Pods[podUID]
	if pod.Devices == nil {
		pod.PodUID = podUID
		pod.Devices = map[string]DeviceConfig{}
	}
	pod.Devices[deviceName] = cfg
	s.Pods[podUID] = pod
	return s.persistLocked()
}

func (s *Store) SetNetNS(podUID types.UID, netns string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod := s.Pods[podUID]
	pod.PodUID = podUID
	pod.NetNS = netns
	s.Pods[podUID] = pod
	return s.persistLocked()
}

func (s *Store) GetPod(podUID types.UID) (PodConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod, ok := s.Pods[podUID]
	return pod, ok
}

func (s *Store) PodConfigs() map[types.UID]PodConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[types.UID]PodConfig, len(s.Pods))
	for uid, pod := range s.Pods {
		result[uid] = pod
	}
	return result
}

func (s *Store) DeletePod(podUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Pods, podUID)
	return s.persistLocked()
}

func (s *Store) DeleteClaim(claimUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for podUID, pod := range s.Pods {
		for deviceName, cfg := range pod.Devices {
			if cfg.ClaimUID == claimUID {
				delete(pod.Devices, deviceName)
			}
		}
		if len(pod.Devices) == 0 {
			delete(s.Pods, podUID)
		} else {
			s.Pods[podUID] = pod
		}
	}
	delete(s.IPAllocations, claimUID)
	return s.persistLocked()
}

func (s *Store) ReserveIP(pool, address string, claimUID, podUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.IPAllocations[claimUID]; ok {
		if current.Pool == pool && current.Address == address {
			return nil
		}
		return errors.New("claim already has a different IP allocation")
	}
	s.IPAllocations[claimUID] = IPAllocation{
		Pool:     pool,
		Address:  address,
		ClaimUID: claimUID,
		PodUID:   podUID,
	}
	return s.persistLocked()
}

func (s *Store) AllocationForClaim(claimUID types.UID) (IPAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.IPAllocations[claimUID]
	return allocation, ok
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.State, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
