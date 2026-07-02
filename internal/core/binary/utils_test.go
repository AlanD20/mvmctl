package binary

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"strips_v", "v1.15.0", "1.15.0"},
		{"no_v", "1.15.0", "1.15.0"},
		{"empty", "", ""},
		{"only_v", "v", ""},
		{"v_in_middle", "1v.0.0", "1v.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeVersion(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCIVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"full_semver", "1.15.0", "v1.15"},
		{"two_parts", "2.0", "v2.0"},
		{"single_part", "42", "v42"},
		{"with_v", "v1.15.0", "vv1.15"},
		{"empty", "", "v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CIVersion(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRustTargetTriple(t *testing.T) {
	got := rustTargetTriple()
	// Should return something reasonable for the current arch
	assert.Contains(t, got, "-unknown-linux-musl")
}
