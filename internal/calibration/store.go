package calibration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultProfileStorePath    = ".guardian-profiles.json"
	DefaultProfileWorkloadType = "xtts"
	UnknownProfileGPUID        = "unknown"
)

type profileStoreDocument struct {
	Profiles map[string]map[string]Profile `json:"profiles"`
}

// LoadProfile returns a persisted profile for the specified GPU and workload type.
//
// When the store file does not exist, it returns (zero, false, nil) to indicate no
// persisted profile is available.
func LoadProfile(path, gpuID, workloadType string) (Profile, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Profile{}, false, nil
	}
	gpuID = normalizeProfileGPU(gpuID)
	workloadType = normalizeProfileWorkload(workloadType)

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Profile{}, false, nil
		}
		return Profile{}, false, err
	}

	var store profileStoreDocument
	if err := json.Unmarshal(raw, &store); err != nil {
		return Profile{}, false, fmt.Errorf("decode profile store: %w", err)
	}

	if len(store.Profiles) == 0 {
		return Profile{}, false, nil
	}
	byGPU, ok := store.Profiles[gpuID]
	if !ok {
		return Profile{}, false, nil
	}
	profile, ok := byGPU[workloadType]
	return profile, ok, nil
}

// SaveProfile persists the profile for the specified GPU and workload type.
func SaveProfile(path, gpuID, workloadType string, profile Profile) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	gpuID = normalizeProfileGPU(gpuID)
	workloadType = normalizeProfileWorkload(workloadType)

	var store profileStoreDocument
	existing, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(existing, &store); err != nil {
			return fmt.Errorf("decode profile store: %w", err)
		}
	}
	if store.Profiles == nil {
		store.Profiles = map[string]map[string]Profile{}
	}

	if store.Profiles[gpuID] == nil {
		store.Profiles[gpuID] = map[string]Profile{}
	}
	store.Profiles[gpuID][workloadType] = profile

	if err := ensureProfileStoreDir(path); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode profile store: %w", err)
	}
	return os.WriteFile(path, raw, 0o600)
}

func ensureProfileStoreDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func normalizeProfileGPU(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return UnknownProfileGPUID
	}
	return value
}

func normalizeProfileWorkload(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultProfileWorkloadType
	}
	return value
}
