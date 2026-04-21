"""Unit tests for the partition detection system.

Tests all detector classes and the RootPartitionDetector integration.
Uses mocked partition data - no real disk images required.
"""

from unittest.mock import MagicMock

import pytest

from mvmctl.utils.partition_detection import (
    FilesystemDetector,
    LabelDetector,
    RootPartitionDetector,
    SizeDetector,
    TypeCodeDetector,
)
from mvmctl.exceptions import RootPartitionDetectionError, TieDetectedError

# -----------------------------------------------------------------------------
# TypeCodeDetector Tests
# -----------------------------------------------------------------------------


class TestTypeCodeDetector:
    """Tests for TypeCodeDetector - GPT/MBR type code detection."""

    def test_gpt_root_x86_64(self):
        """GPT root x86_64 GUID should score 1.0."""
        detector = TypeCodeDetector()
        partition = {
            "type": "44479540-f297-41b2-9af7-d131d5f0458a",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_gpt_root_aarch64(self):
        """GPT root aarch64 GUID should score 1.0."""
        detector = TypeCodeDetector()
        partition = {
            "type": "4f68bce3-e8cd-4db1-96e7-fbcaf984b709",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_mbr_linux(self):
        """MBR Linux type (0x83) should score 0.5."""
        detector = TypeCodeDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_gpt_esp_exclusion(self):
        """GPT ESP GUID should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_gpt_swap_exclusion(self):
        """GPT swap GUID should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_efi_exclusion(self):
        """MBR EFI type (0xef) should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_lvm_exclusion(self):
        """MBR LVM type (0x8e) should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "8e",
            "name": "lvm",
            "size": 41943040,
            "fstype": "LVM2_member",
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_extended_exclusion(self):
        """MBR extended type (0x85) should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "85",
            "name": "extended",
            "size": 83886080,
            "fstype": None,
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_swap_exclusion(self):
        """MBR swap type (0x82) should score -1.0 (exclude)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "82",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = detector.score(partition, [partition])
        assert score == -1.0

    def test_unknown_type(self):
        """Unknown type code should score 0.0 (neutral)."""
        detector = TypeCodeDetector()
        partition = {
            "type": "unknown-guid-1234",
            "name": "data",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_missing_type_field(self):
        """Missing type field should score 0.0 (neutral)."""
        detector = TypeCodeDetector()
        partition = {
            "name": "data",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_case_insensitive_gpt(self):
        """GPT GUIDs should be case-insensitive."""
        detector = TypeCodeDetector()
        partition_upper = {
            "type": "44479540-F297-41B2-9AF7-D131D5F0458A",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        partition_mixed = {
            "type": "4F68BCE3-E8CD-4DB1-96E7-fbcaf984b709",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        assert detector.score(partition_upper, [partition_upper]) == 1.0
        assert detector.score(partition_mixed, [partition_mixed]) == 1.0


# -----------------------------------------------------------------------------
# LabelDetector Tests
# -----------------------------------------------------------------------------


class TestLabelDetector:
    """Tests for LabelDetector - filesystem label detection."""

    def test_root_label(self):
        """Label containing 'root' should score 1.0."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_cloudimg_label(self):
        """Label containing 'cloudimg' should score 1.0."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "cloudimg-rootfs",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_rootfs_label(self):
        """Label containing 'rootfs' should score 1.0."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "rootfs",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_esp_label_negative(self):
        """Label containing 'esp' should score -0.5."""
        detector = LabelDetector()
        partition = {
            "type": "ef",
            "name": "ESP",
            "size": 524288,
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_efi_label_negative(self):
        """Label containing 'efi' should score -0.5."""
        detector = LabelDetector()
        partition = {
            "type": "ef",
            "name": "EFI System",
            "size": 524288,
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_boot_label_negative(self):
        """Label containing 'boot' should score -0.5."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "boot",
            "size": 1048576,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_swap_label_negative(self):
        """Label containing 'swap' should score -0.5."""
        detector = LabelDetector()
        partition = {
            "type": "82",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_neutral_label(self):
        """Neutral label should score 0.0."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "data",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_missing_name_and_label(self):
        """Missing name and label should score 0.0."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_label_field_fallback(self):
        """Should use 'label' field if 'name' is missing."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "label": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 1.0

    def test_name_takes_precedence_over_label(self):
        """Name field should take precedence over label field."""
        detector = LabelDetector()
        partition = {
            "type": "83",
            "name": "data",
            "label": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        # Should use 'name' which is neutral
        assert score == 0.0


# -----------------------------------------------------------------------------
# SizeDetector Tests
# -----------------------------------------------------------------------------


class TestSizeDetector:
    """Tests for SizeDetector - partition size-based detection."""

    def test_largest_valid_partition(self):
        """Largest partition >= MIN_ROOT_SIZE_MB should score 0.5."""
        detector = SizeDetector()
        partitions = [
            {"type": "83", "name": "small", "size": 204800, "fstype": "ext4"},  # 100MB
            {"type": "83", "name": "large", "size": 41943040, "fstype": "ext4"},  # 20GB
        ]
        score = detector.score(partitions[1], partitions)
        assert score == 0.5

    def test_non_largest_valid_partition(self):
        """Valid partition that is not largest should score 0.3."""
        detector = SizeDetector()
        partitions = [
            {"type": "83", "name": "larger", "size": 83886080, "fstype": "ext4"},  # 40GB
            {"type": "83", "name": "smaller", "size": 41943040, "fstype": "ext4"},  # 20GB
        ]
        score = detector.score(partitions[1], partitions)
        assert score == 0.3

    def test_too_small_partition(self):
        """Partition < SIZE_TOO_SMALL_MB should score -0.5."""
        detector = SizeDetector()
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 102400,  # 50MB
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_medium_size_neutral(self):
        """Medium size partition (100-500MB) that is not largest should score 0.0."""
        detector = SizeDetector()
        partitions = [
            {"type": "83", "name": "large", "size": 41943040, "fstype": "ext4"},
            {"type": "83", "name": "medium", "size": 409600, "fstype": "ext4"},  # 200MB
        ]
        score = detector.score(partitions[1], partitions)
        assert score == 0.0

    def test_single_partition_large(self):
        """Single large partition should score 0.5."""
        detector = SizeDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,  # 20GB
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_single_partition_small(self):
        """Single small partition should score -0.5."""
        detector = SizeDetector()
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 102400,  # 50MB
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5

    def test_missing_size_field(self):
        """Missing size field should be treated as too small."""
        detector = SizeDetector()
        partition = {
            "type": "83",
            "name": "root",
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        # Missing size is treated as too small
        assert score == -0.5


# -----------------------------------------------------------------------------
# FilesystemDetector Tests
# -----------------------------------------------------------------------------


class TestFilesystemDetector:
    """Tests for FilesystemDetector - filesystem type detection."""

    def test_ext4_positive(self):
        """ext4 filesystem should score 0.5."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_btrfs_positive(self):
        """btrfs filesystem should score 0.5."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "btrfs",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_xfs_positive(self):
        """xfs filesystem should score 0.5."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "xfs",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_f2fs_positive(self):
        """f2fs filesystem should score 0.5."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "f2fs",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5

    def test_vfat_negative(self):
        """vfat filesystem should score -0.8."""
        detector = FilesystemDetector()
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        score = detector.score(partition, [partition])
        assert score == -0.8

    def test_crypto_luks_neutral(self):
        """crypto_LUKS filesystem should score 0.0."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "encrypted",
            "size": 41943040,
            "fstype": "crypto_LUKS",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_missing_fstype(self):
        """Missing fstype should score 0.0."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
        }
        score = detector.score(partition, [partition])
        assert score == 0.0

    def test_unknown_fstype(self):
        """Unknown filesystem type should score 0.0."""
        detector = FilesystemDetector()
        partition = {
            "type": "83",
            "name": "data",
            "size": 41943040,
            "fstype": "ntfs",
        }
        score = detector.score(partition, [partition])
        assert score == 0.0


# -----------------------------------------------------------------------------
# RootPartitionDetector Tests
# -----------------------------------------------------------------------------


class TestRootPartitionDetector:
    """Tests for RootPartitionDetector - integration and orchestration."""

    def test_single_partition_auto_select(self):
        """Single partition should return partition number 1."""
        detector = RootPartitionDetector()
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed partition number

    def test_clear_winner(self):
        """Clear winner should be selected based on highest score."""
        detector = RootPartitionDetector()
        partitions = [
            # ESP partition - should be excluded
            {"type": "ef", "name": "EFI", "size": 524288, "fstype": "vfat"},
            # Root partition - should win
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "root",
                "size": 41943040,
                "fstype": "ext4",
            },
            # Swap partition - should be excluded
            {"type": "82", "name": "swap", "size": 2097152, "fstype": "swap"},
        ]
        result = detector.detect(partitions)
        assert result == 2  # Root partition number (1-indexed)

    def test_tie_detection_raises_error(self):
        """Tie should raise TieDetectedError."""
        detector = RootPartitionDetector()
        # Two identical partitions with same score
        partitions = [
            {"type": "83", "name": "root1", "size": 41943040, "fstype": "ext4"},
            {"type": "83", "name": "root2", "size": 41943040, "fstype": "ext4"},
        ]
        with pytest.raises(TieDetectedError) as exc_info:
            detector.detect(partitions)
        assert "tie" in str(exc_info.value).lower()

    def test_all_negative_scores_raises_error(self):
        """All partitions with negative scores should raise RootPartitionDetectionError."""
        detector = RootPartitionDetector()
        partitions = [
            {"type": "ef", "name": "EFI", "size": 524288, "fstype": "vfat"},
            {"type": "82", "name": "swap", "size": 2097152, "fstype": "swap"},
        ]
        with pytest.raises(RootPartitionDetectionError) as exc_info:
            detector.detect(partitions)
        assert "no suitable root partition" in str(exc_info.value).lower()

    def test_empty_partitions_raises_error(self):
        """Empty partition list should raise ValueError from max() on empty iterable."""
        detector = RootPartitionDetector()
        with pytest.raises(ValueError):
            detector.detect([])

    def test_weighted_scoring(self):
        """Verify weighted scoring produces correct result."""
        detector = RootPartitionDetector()
        # Partition 0: Good type, but small size
        # Partition 1: Good type, large size, root label
        partitions = [
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "small",
                "size": 1048576,  # 512MB
                "fstype": "ext4",
            },
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "root",
                "size": 41943040,  # 20GB
                "fstype": "ext4",
            },
        ]
        result = detector.detect(partitions)
        assert result == 2  # Larger root partition should win (1-indexed)

    def test_register_custom_detector(self):
        """Custom detector can be registered."""
        detector = RootPartitionDetector()
        custom = MagicMock()
        custom.name = "custom"
        custom.weight = 0.5
        custom.score.return_value = 1.0

        detector.register(custom)

        # Use multiple partitions to trigger scoring logic
        partitions = [
            {"type": "83", "name": "data", "size": 41943040, "fstype": "ext4"},
            {"type": "83", "name": "root", "size": 83886080, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 2  # Second partition should win (1-indexed)
        custom.score.assert_called()


# -----------------------------------------------------------------------------
# Disabled Detectors Tests
# -----------------------------------------------------------------------------


class TestDisabledDetectors:
    """Tests for disabled_detectors parameter."""

    def test_disable_single_detector(self):
        """Disabling one detector should still allow detection."""
        detector = RootPartitionDetector(disabled_detectors=["type_code"])
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed

    def test_disable_multiple_detectors(self):
        """Disabling multiple detectors should still allow detection."""
        detector = RootPartitionDetector(disabled_detectors=["type_code", "filesystem"])
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed

    def test_disable_all_detectors_raises_error(self):
        """Disabling all detectors should still work for single partition."""
        detector = RootPartitionDetector(
            disabled_detectors=["type_code", "label", "size", "filesystem"]
        )
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        # Single partition should still be auto-selected (1-indexed)
        result = detector.detect(partitions)
        assert result == 1

    def test_disable_nonexistent_detector_ignored(self):
        """Disabling non-existent detector should be ignored."""
        detector = RootPartitionDetector(disabled_detectors=["nonexistent"])
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed

    def test_disabled_detectors_affect_scoring(self):
        """Disabled detectors should not contribute to scoring."""
        # Without type_code detector, a partition with good type but bad label
        # might score differently
        detector = RootPartitionDetector(disabled_detectors=["label"])
        partitions = [
            {"type": "83", "name": "data", "size": 41943040, "fstype": "ext4"},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed


# -----------------------------------------------------------------------------
# Edge Cases and Integration Tests
# -----------------------------------------------------------------------------


class TestEdgeCases:
    """Edge cases and integration scenarios."""

    def test_realistic_ubuntu_image(self):
        """Test with realistic Ubuntu cloud image partition layout."""
        detector = RootPartitionDetector()
        partitions = [
            # EFI System Partition
            {
                "type": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
                "name": "EFI System Partition",
                "size": 1048576,  # 512MB
                "fstype": "vfat",
            },
            # Root partition (GPT type, large, ext4)
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "cloudimg-rootfs",
                "size": 83886080,  # 40GB
                "fstype": "ext4",
            },
        ]
        result = detector.detect(partitions)
        assert result == 2  # Root partition should win (1-indexed)

    def test_realistic_centos_image(self):
        """Test with realistic CentOS/RHEL partition layout with only exclusions."""
        detector = RootPartitionDetector()
        partitions = [
            # EFI
            {"type": "ef", "name": "EFI System", "size": 655360, "fstype": "vfat"},
            # Swap
            {"type": "82", "name": "swap", "size": 2097152, "fstype": "swap"},
            # Root (LVM - should be excluded)
            {"type": "8e", "name": "lvm", "size": 41943040, "fstype": "LVM2_member"},
        ]
        # All negative scores or exclusions - should raise error
        with pytest.raises(RootPartitionDetectionError):
            detector.detect(partitions)

    def test_partition_with_all_positive_indicators(self):
        """Partition with all positive indicators should win decisively."""
        detector = RootPartitionDetector()
        partitions = [
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",  # GPT root
                "name": "root",  # Positive label
                "size": 83886080,  # 40GB, largest
                "fstype": "ext4",  # Positive filesystem
            },
            {
                "type": "83",
                "name": "data",
                "size": 41943040,  # 20GB
                "fstype": "ext4",
            },
        ]
        result = detector.detect(partitions)
        assert result == 1  # First partition should win (1-indexed)

    def test_partition_with_mixed_indicators(self):
        """Partition with mixed indicators."""
        detector = RootPartitionDetector()
        partitions = [
            # Good type but small
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "small",
                "size": 204800,  # 100MB
                "fstype": "ext4",
            },
            # MBR type but large and root label
            {
                "type": "83",
                "name": "root",
                "size": 41943040,  # 20GB
                "fstype": "ext4",
            },
        ]
        result = detector.detect(partitions)
        # Second should win due to size and label despite MBR type (1-indexed)
        assert result == 2

    def test_case_sensitivity_in_labels(self):
        """Label detection should be case-insensitive."""
        detector = LabelDetector()

        upper = {"type": "83", "name": "ROOT", "size": 41943040, "fstype": "ext4"}
        lower = {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"}
        mixed = {"type": "83", "name": "Root", "size": 41943040, "fstype": "ext4"}

        assert detector.score(upper, [upper]) == 1.0
        assert detector.score(lower, [lower]) == 1.0
        assert detector.score(mixed, [mixed]) == 1.0

    def test_zero_size_partition(self):
        """Zero-size partition should be handled."""
        detector = SizeDetector()
        partition = {
            "type": "83",
            "name": "empty",
            "size": 0,
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == -0.5  # Too small

    def test_very_large_partition(self):
        """Very large partition should be handled."""
        detector = SizeDetector()
        partition = {
            "type": "83",
            "name": "huge",
            "size": 2147483648,  # 1TB
            "fstype": "ext4",
        }
        score = detector.score(partition, [partition])
        assert score == 0.5  # Largest and valid size

    def test_none_values_in_partition(self):
        """None values in partition dict should be handled gracefully."""
        detector = RootPartitionDetector()
        partitions = [
            {
                "type": None,
                "name": None,
                "size": None,
                "fstype": None,
            },
        ]
        # Single partition should still be selected despite None values (1-indexed)
        result = detector.detect(partitions)
        assert result == 1

    def test_partial_partition_data(self):
        """Partition with partial data should work."""
        detector = RootPartitionDetector()
        partitions = [
            {"size": 41943040},
        ]
        result = detector.detect(partitions)
        assert result == 1  # 1-indexed partition number
