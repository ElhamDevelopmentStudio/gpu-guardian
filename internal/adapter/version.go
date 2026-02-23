package adapter

import (
	"fmt"
	"strconv"
	"strings"
)

// AdapterInterfaceVersion is the current stable adapter API major version.
const AdapterInterfaceVersion = "v1"
const AdapterVersion = AdapterInterfaceVersion

// VersionedAdapter is implemented by adapters that can report their protocol version.
type VersionedAdapter interface {
	AdapterAPIVersion() string
}

// AdapterVersionError is returned when adapter versions are incompatible.
type AdapterVersionError struct {
	Expected string
	Actual   string
}

func (e AdapterVersionError) Error() string {
	return fmt.Sprintf("incompatible adapter version: expected %q, got %q", e.Expected, e.Actual)
}

// ParseAdapterMajor parses the major version token from vN style strings.
func ParseAdapterMajor(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty adapter version")
	}
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid adapter major version %q: %w", v, err)
	}
	if major <= 0 {
		return 0, fmt.Errorf("invalid adapter major version %q", v)
	}
	return major, nil
}

// ValidateAdapterVersion validates adapter major compatibility.
func ValidateAdapterVersion(version string) error {
	got, err := ParseAdapterMajor(version)
	if err != nil {
		return err
	}
	expected, err := ParseAdapterMajor(AdapterInterfaceVersion)
	if err != nil {
		return err
	}
	if got != expected {
		return AdapterVersionError{
			Expected: AdapterInterfaceVersion,
			Actual:   version,
		}
	}
	return nil
}

// ValidateVersionedAdapter checks whether the given versioned adapter is supported by the current major interface.
func ValidateVersionedAdapter(a VersionedAdapter) error {
	if a == nil {
		return fmt.Errorf("adapter is nil")
	}
	return ValidateAdapterVersion(a.AdapterAPIVersion())
}
