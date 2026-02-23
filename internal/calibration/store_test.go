package calibration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfileReturnsMissingForNoStore(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "profiles.json")

	_, found, err := LoadProfile(path, "GPU-1", "xtts")
	if err != nil {
		t.Fatalf("expected no load error: %v", err)
	}
	if found {
		t.Fatalf("expected missing profile for nonexistent store")
	}
}

func TestSaveAndLoadProfileByGPUAndWorkload(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "profiles.json")

	profile := Profile{
		GPUUUID:                "GPU-AAA",
		Command:                "python generate_xtts.py",
		WorkloadType:           "xtts",
		BaselineThroughput:     123.4,
		SafeConcurrencyCeiling: 4,
	}
	profile2 := Profile{
		GPUUUID:                "GPU-BBB",
		Command:                "python generate_xtts.py",
		WorkloadType:           "other",
		BaselineThroughput:     98.7,
		SafeConcurrencyCeiling: 2,
	}

	if err := SaveProfile(path, "GPU-AAA", "xtts", profile); err != nil {
		t.Fatalf("save primary profile: %v", err)
	}
	if err := SaveProfile(path, "GPU-BBB", "other", profile2); err != nil {
		t.Fatalf("save secondary profile: %v", err)
	}

	got1, found, err := LoadProfile(path, "GPU-AAA", "xtts")
	if err != nil {
		t.Fatalf("load profile 1: %v", err)
	}
	if !found {
		t.Fatalf("expected profile for GPU-AAA/xtts")
	}
	if got1.GPUUUID != profile.GPUUUID || got1.SafeConcurrencyCeiling != profile.SafeConcurrencyCeiling {
		t.Fatalf("unexpected loaded profile: %#v", got1)
	}

	got2, found, err := LoadProfile(path, "GPU-BBB", "other")
	if err != nil {
		t.Fatalf("load profile 2: %v", err)
	}
	if !found {
		t.Fatalf("expected profile for GPU-BBB/other")
	}
	if got2.BaselineThroughput != profile2.BaselineThroughput {
		t.Fatalf("unexpected loaded profile: %#v", got2)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected persisted profile store to be non-empty")
	}
}

func TestSaveProfileDefaultsUnknownKeys(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "profiles.json")

	if err := SaveProfile(path, "", "", Profile{Command: "cmd"}); err != nil {
		t.Fatalf("save profile with defaults: %v", err)
	}

	_, found, err := LoadProfile(path, UnknownProfileGPUID, DefaultProfileWorkloadType)
	if err != nil {
		t.Fatalf("load default-keyed profile: %v", err)
	}
	if !found {
		t.Fatalf("expected default-keyed profile to be saved")
	}
}
