package crypto_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/crypto"
)

// --- ContentHash ---
// Rationale: Deterministic SHA-256 hex digest. Same inputs → same output.
// Different inputs → different output.

func TestContentHash(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.ContentHash("hello", "world")
		b := crypto.ContentHash("hello", "world")
		assert.Equal(t, a, b)
	})

	t.Run("different_inputs_different_outputs", func(t *testing.T) {
		a := crypto.ContentHash("hello", "world")
		b := crypto.ContentHash("hello", "earth")
		assert.NotEqual(t, a, b)
	})

	t.Run("empty_parts", func(t *testing.T) {
		got := crypto.ContentHash()
		assert.Len(t, got, 64)
	})

	t.Run("single_part", func(t *testing.T) {
		got := crypto.ContentHash("test")
		assert.Len(t, got, 64)
	})
}

// --- ImageID ---
// Rationale: Deterministic from type, source, timestamp.

func TestImageID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.ImageID("qcow2", "http://example.com/img", "2024-01-01T00:00:00Z")
		b := crypto.ImageID("qcow2", "http://example.com/img", "2024-01-01T00:00:00Z")
		assert.Equal(t, a, b)
	})

	t.Run("different_source_different_id", func(t *testing.T) {
		a := crypto.ImageID("qcow2", "source-a", "2024-01-01T00:00:00Z")
		b := crypto.ImageID("qcow2", "source-b", "2024-01-01T00:00:00Z")
		assert.NotEqual(t, a, b)
	})

	t.Run("returns_64_char_hex", func(t *testing.T) {
		got := crypto.ImageID("raw", "src", "ts")
		assert.Len(t, got, 64)
	})
}

// --- VMID ---
// Rationale: 32-char truncated SHA256 (for Unix socket path limits).

func TestVMID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.VMID("test-vm", "2024-01-01T00:00:00Z")
		b := crypto.VMID("test-vm", "2024-01-01T00:00:00Z")
		assert.Equal(t, a, b)
	})

	t.Run("returns_32_chars", func(t *testing.T) {
		got := crypto.VMID("test-vm", "2024-01-01T00:00:00Z")
		assert.Len(t, got, 32)
	})

	t.Run("different_names_different_ids", func(t *testing.T) {
		a := crypto.VMID("vm-1", "2024-01-01T00:00:00Z")
		b := crypto.VMID("vm-2", "2024-01-01T00:00:00Z")
		assert.NotEqual(t, a, b)
	})
}

// --- NetworkID ---
// Rationale: 64-char SHA256 from name, subnet, timestamp.

func TestNetworkID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.NetworkID("default", "10.0.0.0/24", "2024-01-01T00:00:00Z")
		b := crypto.NetworkID("default", "10.0.0.0/24", "2024-01-01T00:00:00Z")
		assert.Equal(t, a, b)
	})

	t.Run("returns_64_chars", func(t *testing.T) {
		got := crypto.NetworkID("n", "10.0.0.0/24", "ts")
		assert.Len(t, got, 64)
	})
}

// --- BatchID ---
// Rationale: 16-char truncated SHA256 (short for cache dir paths).

func TestBatchID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.BatchID("batch-1", "2024-01-01T00:00:00Z")
		b := crypto.BatchID("batch-1", "2024-01-01T00:00:00Z")
		assert.Equal(t, a, b)
	})

	t.Run("returns_16_chars", func(t *testing.T) {
		got := crypto.BatchID("batch-1", "2024-01-01T00:00:00Z")
		assert.Len(t, got, 16)
	})
}

// --- VolumeID ---
// Rationale: 64-char SHA256 from name and timestamp.

func TestVolumeID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := crypto.VolumeID("vol-1", "2024-01-01T00:00:00Z")
		b := crypto.VolumeID("vol-1", "2024-01-01T00:00:00Z")
		assert.Equal(t, a, b)
	})

	t.Run("returns_64_chars", func(t *testing.T) {
		got := crypto.VolumeID("vol-1", "2024-01-01T00:00:00Z")
		assert.Len(t, got, 64)
	})
}

// --- SnapshotID ---
// Rationale: SnapshotID generates a 64-char SHA256 hex digest from source VM
// ID and creation timestamp. Bugs here cause snapshot identity collisions or
// lookup failures — the ID is the primary key in the snapshots table.

func TestSnapshotID(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		// CONTRACT: Same inputs always produce same output.
		// Expected value is computed by a known SHA-256 of "vm-1:2024-01-01T00:00:00Z"
		// which is the documented SnapshotID algorithm.
		const want = "bbea08ed860f5d6db9284026d2f50774b22cedbc27790e6e3d08d8d683c9f25c"
		got := crypto.SnapshotID("vm-1", "2024-01-01T00:00:00Z")
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("SnapshotID() determinism mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("different_inputs_different_outputs", func(t *testing.T) {
		a := crypto.SnapshotID("vm-1", "2024-01-01T00:00:00Z")
		b := crypto.SnapshotID("vm-2", "2024-01-01T00:00:00Z")
		if diff := cmp.Diff(a, b); diff == "" {
			t.Errorf("SnapshotID() must produce different outputs for different inputs")
		}
	})

	// Table-driven format and boundary checks (no error return, so no error rows)
	tests := map[string]struct {
		sourceVMID string
		createdAt  string
	}{
		"basic_case":    {sourceVMID: "vm-1", createdAt: "2024-01-01T00:00:00Z"},
		"empty_source":  {sourceVMID: "", createdAt: "2024-01-01T00:00:00Z"},
		"empty_created": {sourceVMID: "vm-1", createdAt: ""},
		"both_empty":    {sourceVMID: "", createdAt: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := crypto.SnapshotID(tc.sourceVMID, tc.createdAt)
			// CONTRACT: Full 64 chars (no truncation) since snapshot IDs are
			// not used in Unix domain socket paths.
			assert.Len(t, got, 64, "SnapshotID must be 64 hex chars")
			// CONTRACT: Output is lowercase hex (fmt.Sprintf("%x", ...))
			assert.Regexp(t, "^[0-9a-f]{64}$", got,
				"SnapshotID must be 64 lowercase hex characters")
		})
	}
}

// --- ShortenID ---
// Rationale: Returns first N chars. Errors if ID is shorter than requested length.

func TestShortenID(t *testing.T) {
	t.Run("default_length", func(t *testing.T) {
		id := "abcdef1234567890"
		got, err := crypto.ShortenID(id)
		require.NoError(t, err)
		assert.Equal(t, "abcdef123456", got) // 12 chars
	})

	t.Run("custom_length", func(t *testing.T) {
		id := "abcdef1234567890"
		got, err := crypto.ShortenID(id, 8)
		require.NoError(t, err)
		assert.Equal(t, "abcdef12", got)
	})

	t.Run("id_shorter_than_length_errors", func(t *testing.T) {
		_, err := crypto.ShortenID("abc", 10)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shorter than requested length")
	})

	t.Run("full_length_id", func(t *testing.T) {
		got, err := crypto.ShortenID("abcdef", 6)
		require.NoError(t, err)
		assert.Equal(t, "abcdef", got)
	})
}

// --- Truncate ---
// Rationale: Returns first n chars. If shorter, returns unchanged. Never errors.

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"longer", "abcdef", 3, "abc"},
		{"exact", "abc", 3, "abc"},
		{"shorter", "ab", 3, "ab"},
		{"empty", "", 5, ""},
		{"zero_n", "abc", 0, ""},
		// negative n excluded — panics (slice bounds out of range)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := crypto.Truncate(tt.s, tt.n)
			assert.Equal(t, tt.want, got)
		})
	}
}
