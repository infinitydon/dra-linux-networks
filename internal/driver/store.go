package driver

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

type Store struct {
	path string
	mu   sync.Mutex
	pods map[types.UID]PodConfig
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, pods: map[types.UID]PodConfig{}}
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
		if err := json.Unmarshal(data, &s.pods); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) SetDevice(podUID types.UID, deviceName string, cfg DeviceConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod := s.pods[podUID]
	if pod.Devices == nil {
		pod.PodUID = podUID
		pod.Devices = map[string]DeviceConfig{}
	}
	pod.Devices[deviceName] = cfg
	s.pods[podUID] = pod
	return s.persistLocked()
}

func (s *Store) SetNetNS(podUID types.UID, netns string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod := s.pods[podUID]
	pod.PodUID = podUID
	pod.NetNS = netns
	s.pods[podUID] = pod
	return s.persistLocked()
}

func (s *Store) GetPod(podUID types.UID) (PodConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pod, ok := s.pods[podUID]
	return pod, ok
}

func (s *Store) DeletePod(podUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, podUID)
	return s.persistLocked()
}

func (s *Store) DeleteClaim(claimUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for podUID, pod := range s.pods {
		for deviceName, cfg := range pod.Devices {
			if cfg.ClaimUID == claimUID {
				delete(pod.Devices, deviceName)
			}
		}
		if len(pod.Devices) == 0 {
			delete(s.pods, podUID)
		} else {
			s.pods[podUID] = pod
		}
	}
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.pods, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
