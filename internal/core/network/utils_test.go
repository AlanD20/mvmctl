package network_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
)

// --- ComputeBridgeAddress ----------------------------------------------------
// Rationale: ComputeBridgeAddress validates the subnet CIDR and returns a
// gateway+prefix pair used for bridge interface addressing.

func TestUtilsComputeBridgeAddress(t *testing.T) {
	tests := map[string]struct {
		gateway string
		subnet  string
		want    string
		wantErr bool
	}{
		// Error paths first
		"empty subnet": {
			gateway: "10.0.0.1",
			subnet:  "",
			wantErr: true,
		},
		"invalid cidr string": {
			gateway: "10.0.0.1",
			subnet:  "not-a-cidr",
			wantErr: true,
		},
		"subnet without prefix": {
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0",
			wantErr: true,
		},
		"prefix too large": {
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0/33",
			wantErr: true,
		},
		// Happy paths
		"/24 subnet": {
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0/24",
			want:    "10.0.0.1/24",
		},
		"/16 subnet": {
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0/16",
			want:    "10.0.0.1/16",
		},
		"/32 subnet": {
			gateway: "10.0.0.1",
			subnet:  "10.0.0.0/32",
			want:    "10.0.0.1/32",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := network.ComputeBridgeAddress(tc.gateway, tc.subnet)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ComputeBridgeAddress mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- ComputeBridgeName -------------------------------------------------------
// Rationale: Bridge names are limited to 15 characters by Linux. Short names
// pass through directly; long names are truncated and suffixed with a hash.

func TestUtilsComputeBridgeName(t *testing.T) {
	tests := map[string]struct {
		networkName string
		want        string
	}{
		"short name returns mvm-<name>": {
			networkName: "test",
			want:        "mvm-test",
		},
		"exactly 15 chars — no hash": {
			networkName: "abcdefghijk", // "mvm-" + 11 = 15
			want:        "mvm-abcdefghijk",
		},
		"empty string": {
			networkName: "",
			want:        "mvm-",
		},
		"long name truncated with hash": {
			networkName: "verylongnetworkname",
			want:        "mvm-ve-cf3c4258", // pre-computed deterministic value
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := network.ComputeBridgeName(tc.networkName)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ComputeBridgeName mismatch (-want +got):\n%s", diff)
			}
			// Every bridge name must respect the 15-char kernel limit.
			assert.LessOrEqual(t, len(got), 15,
				"bridge name must not exceed 15 characters")
		})
	}
}

// Rationale: Identical inputs must always produce the same bridge name so that
// the bridge device can be reliably located across invocations.
func TestComputeBridgeName_deterministic(t *testing.T) {
	name := "workload"
	a := network.ComputeBridgeName(name)
	b := network.ComputeBridgeName(name)
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("ComputeBridgeName is not deterministic (-first +second):\n%s", diff)
	}
}

// --- GenerateTAPName ---------------------------------------------------------
// Rationale: TAP device names must be unique per (network, VM) pair to avoid
// conflicts on the host bridge.

func TestGenerateTAPName_format(t *testing.T) {
	tests := map[string]struct {
		networkName string
		vmName      string
	}{
		"basic pair": {
			networkName: "net1",
			vmName:      "myvm1",
		},
		"empty network name": {
			networkName: "",
			vmName:      "myvm1",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := network.GenerateTAPName(tc.networkName, tc.vmName)
			// Must start with "mvm-"
			assert.True(t, strings.HasPrefix(got, "mvm-"),
				"TAP name must start with 'mvm-', got %q", got)
			// Format: "mvm-<11-char-hex>"
			parts := strings.SplitN(got, "-", 2)
			require.Len(t, parts, 2, "TAP name must have exactly one hyphen")
			assert.Len(t, parts[1], 11, "hash portion must be 11 characters")
		})
	}
}

// Rationale: Same inputs must always produce the same TAP name.
func TestGenerateTAPName_deterministic(t *testing.T) {
	a := network.GenerateTAPName("net1", "vm1")
	b := network.GenerateTAPName("net1", "vm1")
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("GenerateTAPName is not deterministic (-first +second):\n%s", diff)
	}
}

// Rationale: Different (network, VM) pairs must produce different names to
// prevent TAP device name collisions on the bridge.
func TestGenerateTAPName_differentInputs(t *testing.T) {
	a := network.GenerateTAPName("net1", "vm1")
	b := network.GenerateTAPName("net2", "vm1")
	if diff := cmp.Diff(a, b); diff == "" {
		t.Errorf("GenerateTAPName should produce different outputs for different inputs, but both returned %q", a)
	}
}

// --- GenerateMAC -------------------------------------------------------------
// Rationale: GenerateMAC creates a random MAC address with the given OUI
// prefix. We verify format constraints only — randomness is validated by the
// crypto/rand source, not by unit tests.

func TestGenerateMAC_format(t *testing.T) {
	tests := map[string]struct {
		prefix string
	}{
		"boundary/empty prefix": {
			prefix: "",
		},
		"default prefix 02:FC": {
			prefix: "02:FC",
		},
		"custom prefix": {
			prefix: "06:AA",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := network.GenerateMAC(tc.prefix)

			// Starts with the given prefix (trivially true for empty).
			assert.True(t, strings.HasPrefix(got, tc.prefix),
				"MAC must start with prefix %q, got %q", tc.prefix, got)

			if tc.prefix == "" {
				// Empty prefix produces format ":XX:XX:XX:XX" (12 chars).
				assert.Len(t, got, 12, "MAC with empty prefix must be 12 characters")
				return
			}

			// Full MAC: XX:XX:XX:XX:XX:XX = 17 chars.
			assert.Len(t, got, 17, "MAC address must be 17 characters")

			// Colon positions for a 6-octet MAC.
			assert.Equal(t, byte(':'), got[2], "colon at position 2")
			assert.Equal(t, byte(':'), got[5], "colon at position 5")
			assert.Equal(t, byte(':'), got[8], "colon at position 8")
			assert.Equal(t, byte(':'), got[11], "colon at position 11")
			assert.Equal(t, byte(':'), got[14], "colon at position 14")

			// All non-colon characters must be uppercase hex digits.
			for i, c := range got {
				if c == ':' {
					continue
				}
				if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
					t.Errorf("MAC byte at position %d is %q — must be uppercase hex", i, c)
				}
			}
		})
	}
}
