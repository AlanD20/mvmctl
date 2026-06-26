package disk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/disk"
	"mvmctl/pkg/errs"
)

// --- ParseDiskSize ---
// Rationale: Pure string-to-bytes parsing with IEC binary units.

func TestParseDiskSize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr string
	}{
		// Error paths first
		{name: "empty_string", input: "", wantErr: "Invalid disk size format"},
		{name: "negative", input: "-1G", wantErr: "Invalid disk size format"},
		{name: "non_numeric", input: "abcG", wantErr: "Invalid disk size format"},
		{name: "unknown_unit", input: "42xyz", wantErr: "Invalid disk size format"},
		// Happy paths
		{name: "bytes_no_unit", input: "1024", want: 1024},
		{name: "zero", input: "0", want: 0},
		{name: "512M", input: "512M", want: 512 * 1024 * 1024},
		{name: "1G", input: "1G", want: 1024 * 1024 * 1024},
		{name: "2.5GB", input: "2.5GB", want: int64(2.5 * 1024 * 1024 * 1024)},
		{name: "1T", input: "1T", want: 1024 * 1024 * 1024 * 1024},
		{name: "1K", input: "1K", want: 1024},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := disk.ParseDiskSize(tc.input)
			if tc.wantErr != "" {
				require.Error(t, err)
				de, ok := errs.AsType[*errs.DomainError](err)
				require.True(t, ok)
				assert.Equal(t, errs.CodeValidationFailed, de.Code)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- FormatDiskSize ---
// Rationale: Reverse of ParseDiskSize — bytes to human-readable string.

func TestFormatDiskSize(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0B"},
		{name: "1K", bytes: 1024, want: "1K"},
		{name: "1.5M", bytes: int64(1.5 * 1024 * 1024), want: "1.5M"},
		{name: "1G", bytes: 1024 * 1024 * 1024, want: "1G"},
		{name: "2G", bytes: 2 * 1024 * 1024 * 1024, want: "2G"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := disk.FormatDiskSize(tc.bytes)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- FormatSectorsHumanReadable ---
// Rationale: Converts sector count + sector size to "X.X MiB/GiB".

func TestFormatSectorsHumanReadable(t *testing.T) {
	tests := []struct {
		name       string
		sectors    int64
		sectorSize int64
		want       string
	}{
		{name: "zero_sectors", sectors: 0, sectorSize: 512, want: "0.0 MiB"},
		{name: "2048_sectors_1MiB", sectors: 2048, sectorSize: 512, want: "1.0 MiB"},
		{name: "1GiB", sectors: 2 * 1024 * 1024, sectorSize: 512, want: "1.0 GiB"},
		{name: "custom_sector_size_4096", sectors: 256, sectorSize: 4096, want: "1.0 MiB"},
		{name: "default_sector_size_0_uses_512", sectors: 2048, sectorSize: 0, want: "1.0 MiB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := disk.FormatSectorsHumanReadable(tc.sectors, tc.sectorSize)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- TypeCodeDetector.Score ---
// Rationale: Scores partitions by partition type GUID/hex code.

func TestTypeCodeDetector_Score(t *testing.T) {
	d := disk.TypeCodeDetector{}
	tests := []struct {
		name      string
		partType  string
		wantScore float64
	}{
		{name: "gpt_root_x86_64", partType: "44479540-f297-41b2-9af7-d131d5f0458a", wantScore: 1.0},
		{name: "gpt_root_aarch64", partType: "4f68bce3-e8cd-4db1-96e7-fbcaf984b709", wantScore: 1.0},
		{name: "mbr_linux_83", partType: "83", wantScore: 0.5},
		{name: "gpt_esp", partType: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", wantScore: -1.0},
		{name: "gpt_swap", partType: "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f", wantScore: -1.0},
		{name: "mbr_efi_ef", partType: "ef", wantScore: -1.0},
		{name: "mbr_swap_82", partType: "82", wantScore: -1.0},
		{name: "mbr_extended_85", partType: "85", wantScore: -1.0},
		{name: "mbr_lvm_8e", partType: "8e", wantScore: -1.0},
		{name: "unknown", partType: "unknown-type", wantScore: 0.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := disk.Partition{Type: tc.partType}
			got := d.Score(p, nil)
			assert.InDelta(t, tc.wantScore, got, 0.001)
		})
	}
}

// --- LabelDetector.Score ---
// Rationale: Scores partitions by label/name containing root or exclude keywords.

func TestLabelDetector_Score(t *testing.T) {
	d := disk.LabelDetector{}
	tests := []struct {
		name  string
		pName string
		label string
		want  float64
	}{
		{name: "label_contains_root", pName: "", label: "myrootfs", want: 1.0},
		{name: "name_contains_root", pName: "rootPart", label: "", want: 1.0},
		{name: "name_contains_cloudimg", pName: "cloudimg-disk", label: "", want: 1.0},
		{name: "label_contains_rootfs", pName: "", label: "rootfs_data", want: 1.0},
		{name: "label_contains_esp", pName: "", label: "ESP", want: -0.5},
		{name: "name_contains_efi", pName: "EFI System", label: "", want: -0.5},
		{name: "name_contains_boot", pName: "boot_partition", label: "", want: -0.5},
		{name: "label_contains_swap", pName: "", label: "Linux Swap", want: -0.5},
		{name: "unknown", pName: "data", label: "", want: 0.0},
		{name: "empty", pName: "", label: "", want: 0.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := disk.Partition{Name: tc.pName, Label: tc.label}
			got := d.Score(p, nil)
			assert.InDelta(t, tc.want, got, 0.001)
		})
	}
}

// --- SizeDetector.Score ---
// Rationale: Scores partitions by size relative to min root / too-small thresholds.

func TestSizeDetector_Score(t *testing.T) {
	d := disk.SizeDetector{}

	tests := []struct {
		name       string
		partitions []disk.Partition
		targetIdx  int
		want       float64
	}{
		{
			name: "largest_above_min_root",
			partitions: []disk.Partition{
				{Size: 1024000}, // 500 MB
				{Size: 2048000}, // 1000 MB → largest
			},
			targetIdx: 1,
			want:      0.5,
		},
		{
			name: "not_largest_above_min_root",
			partitions: []disk.Partition{
				{Size: 2048000}, // 1000 MB → largest
				{Size: 1024000}, // 500 MB → non-largest
			},
			targetIdx: 1,
			want:      0.3,
		},
		{
			name: "between_too_small_and_min_root",
			partitions: []disk.Partition{
				{Size: 204800}, // 100 MB → >= 100, < 500
			},
			targetIdx: 0,
			want:      0.0,
		},
		{
			name: "too_small",
			partitions: []disk.Partition{
				{Size: 102400}, // 50 MB → < 100
			},
			targetIdx: 0,
			want:      -0.5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := d.Score(tc.partitions[tc.targetIdx], tc.partitions)
			assert.InDelta(t, tc.want, got, 0.001)
		})
	}
}

// --- FilesystemDetector.Score ---
// Rationale: Scores partitions by filesystem type.

func TestFilesystemDetector_Score(t *testing.T) {
	d := disk.FilesystemDetector{}
	tests := []struct {
		name   string
		fstype string
		want   float64
	}{
		{name: "ext4", fstype: "ext4", want: 0.5},
		{name: "btrfs", fstype: "btrfs", want: 0.5},
		{name: "xfs", fstype: "xfs", want: 0.5},
		{name: "f2fs", fstype: "f2fs", want: 0.5},
		{name: "vfat", fstype: "vfat", want: -0.8},
		{name: "crypto_luks", fstype: "crypto_luks", want: 0.0},
		{name: "empty", fstype: "", want: 0.0},
		{name: "unknown", fstype: "ntfs", want: 0.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := disk.Partition{Fstype: tc.fstype}
			got := d.Score(p, nil)
			assert.InDelta(t, tc.want, got, 0.001)
		})
	}
}

// --- RootPartitionDetector.Detect ---
// Rationale: Aggregates all detectors to find the root partition index.

func TestRootPartitionDetector_Detect(t *testing.T) {
	t.Run("single_partition_returns_1", func(t *testing.T) {
		d := disk.NewRootPartitionDetector(nil)
		idx, err := d.Detect([]disk.Partition{{Size: 1024000, Type: "83"}})
		require.NoError(t, err)
		assert.Equal(t, 1, idx)
	})

	t.Run("no_partitions_returns_error", func(t *testing.T) {
		d := disk.NewRootPartitionDetector(nil)
		_, err := d.Detect(nil)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeRootPartitionDetection, de.Code)
		return
	})

	t.Run("clear_winner_returns_winning_index", func(t *testing.T) {
		d := disk.NewRootPartitionDetector(nil)
		parts := []disk.Partition{
			{Type: "83", Size: 1024000, Fstype: "ext4", Name: "root"},                                 // should win
			{Type: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", Size: 204800, Fstype: "vfat", Name: "ESP"}, // excluded
		}
		idx, err := d.Detect(parts)
		require.NoError(t, err)
		assert.Equal(t, 1, idx)
	})

	t.Run("tie_returns_error", func(t *testing.T) {
		d := disk.NewRootPartitionDetector(nil)
		// Two identical partitions → tie
		parts := []disk.Partition{
			{Type: "83", Size: 1024000, Fstype: "ext4"},
			{Type: "83", Size: 1024000, Fstype: "ext4"},
		}
		_, err := d.Detect(parts)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeTieDetected, de.Code)
		return
	})

	t.Run("all_negative_scores_returns_error", func(t *testing.T) {
		d := disk.NewRootPartitionDetector(nil)
		// Two partitions with different negative scores (no tie)
		parts := []disk.Partition{
			{
				Type:   "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
				Size:   102400,
				Fstype: "vfat",
			}, // ESP, 50MB, vfat → very negative
			{
				Type:   "83",
				Size:   102400,
				Fstype: "vfat",
			}, // MBR Linux, 50MB, vfat → still < 0
		}
		_, err := d.Detect(parts)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeRootPartitionDetection, de.Code)
		return
	})

	t.Run("disabled_detector_not_used", func(t *testing.T) {
		// Disable size detector so a small partition isn't penalised
		d := disk.NewRootPartitionDetector([]string{"size"})
		parts := []disk.Partition{
			{Type: "83", Size: 102400, Fstype: "ext4"},                                   // small but valid
			{Type: "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", Size: 204800, Fstype: "vfat"}, // ESP
		}
		idx, err := d.Detect(parts)
		require.NoError(t, err)
		assert.Equal(t, 1, idx)
	})
}

// --- getDetectorScore / getDetectorWeight (tested via  ---
// Rationale: Verifies known and unknown keys return correct values.

func TestGetDetectorScore(t *testing.T) {
	t.Run("known_key", func(t *testing.T) {
		got := disk.DetectorScores["root_score"]
		assert.InDelta(t, 1.0, got, 0.001)
	})

	t.Run("unknown_key_default", func(t *testing.T) {
		_, ok := disk.DetectorScores["nonexistent"]
		assert.False(t, ok)
	})
}

func TestGetDetectorWeight(t *testing.T) {
	t.Run("known_key", func(t *testing.T) {
		got := disk.DetectorWeights["type_code"]
		assert.InDelta(t, 1.0, got, 0.001)
	})

	t.Run("unknown_key_default", func(t *testing.T) {
		_, ok := disk.DetectorWeights["nonexistent"]
		assert.False(t, ok)
	})
}
