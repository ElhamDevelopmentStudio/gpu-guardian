package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type SampleStore struct {
	mu   sync.Mutex
	file *os.File
}

func NewSampleStore(path string) (*SampleStore, error) {
	if path == "" {
		return nil, nil
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sample store directory: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &SampleStore{file: f}, nil
}

func (s *SampleStore) Append(sample TelemetrySample) error {
	if s == nil || s.file == nil {
		return nil
	}

	b, err := json.Marshal(sample)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.file.WriteString(string(b) + "\n")
	return err
}

func (s *SampleStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
