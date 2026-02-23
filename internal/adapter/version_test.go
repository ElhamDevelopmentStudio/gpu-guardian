package adapter

import (
	"strings"
	"testing"
)

type stubVersionedAdapter struct {
	version string
}

func (s stubVersionedAdapter) AdapterAPIVersion() string {
	return s.version
}

func TestParseAdapterMajor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{name: "v1", input: "v1", want: 1, wantErr: false},
		{name: "1", input: "1", want: 1, wantErr: false},
		{name: "v1.2", input: "v1.2", want: 1, wantErr: false},
		{name: "blank", input: "", want: 0, wantErr: true},
		{name: "nonnumeric", input: "vX", want: 0, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAdapterMajor(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("major mismatch: expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestValidateAdapterVersion(t *testing.T) {
	t.Parallel()

	if err := ValidateAdapterVersion(AdapterInterfaceVersion); err != nil {
		t.Fatalf("expected adapter interface version to validate, got %v", err)
	}
	if err := ValidateAdapterVersion("v2"); err == nil {
		t.Fatal("expected mismatch for v2")
	}
	if err := ValidateAdapterVersion("  "); err == nil {
		t.Fatal("expected error for blank version")
	}
}

func TestValidateVersionedAdapter(t *testing.T) {
	t.Parallel()

	if err := ValidateVersionedAdapter(stubVersionedAdapter{version: AdapterInterfaceVersion}); err != nil {
		t.Fatalf("expected valid adapter version, got %v", err)
	}
	if err := ValidateVersionedAdapter(stubVersionedAdapter{version: "v0"}); err == nil {
		t.Fatal("expected invalid version rejection")
	}

	nilErr := ValidateVersionedAdapter(nil)
	if nilErr == nil {
		t.Fatal("expected nil adapter validation error")
	}
	if !strings.Contains(nilErr.Error(), "nil") {
		t.Fatalf("expected nil adapter error, got %v", nilErr)
	}
}

func TestXttsAdapterCompatibility(t *testing.T) {
	t.Parallel()

	adapter := NewXttsAdapter(Config{})
	if err := ValidateVersionedAdapter(adapter); err != nil {
		t.Fatalf("expected XTTS adapter to be compatibility-checked successfully, got %v", err)
	}
}
