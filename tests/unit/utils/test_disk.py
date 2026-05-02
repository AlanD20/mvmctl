"""Tests for disk size parsing and root partition detection utilities."""

from __future__ import annotations

import pytest

from mvmctl.exceptions import (
    MVMError,
    RootPartitionDetectionError,
    TieDetectedError,
)
from mvmctl.utils._disk import (
    FilesystemDetector,
    LabelDetector,
    RootPartitionDetector,
    SizeDetector,
    TypeCodeDetector,
    format_disk_size,
    format_sectors_human_readable,
    parse_disk_size,
)

# =========================================================================
# parse_disk_size
# =========================================================================


class TestParseDiskSize:
    """Tests for parse_disk_size function."""

    def test_parse_bytes(self) -> None:
        assert parse_disk_size("512B") == 512
        assert parse_disk_size("1024") == 1024  # no unit → bytes
        assert parse_disk_size("0B") == 0

    def test_parse_kilobytes(self) -> None:
        assert parse_disk_size("1K") == 1024
        assert parse_disk_size("1KB") == 1024
        assert parse_disk_size("512K") == 512 * 1024
        assert parse_disk_size("2.5KB") == int(2.5 * 1024)

    def test_parse_megabytes(self) -> None:
        assert parse_disk_size("1M") == 1024**2
        assert parse_disk_size("1MB") == 1024**2
        assert parse_disk_size("512M") == 512 * 1024**2
        assert parse_disk_size("2.5MB") == int(2.5 * 1024**2)

    def test_parse_gigabytes(self) -> None:
        assert parse_disk_size("1G") == 1024**3
        assert parse_disk_size("1GB") == 1024**3
        assert parse_disk_size("2G") == 2 * 1024**3
        assert parse_disk_size("2.5GB") == int(2.5 * 1024**3)

    def test_parse_terabytes(self) -> None:
        assert parse_disk_size("1T") == 1024**4
        assert parse_disk_size("1TB") == 1024**4
        assert parse_disk_size("2T") == 2 * 1024**4

    def test_case_insensitive(self) -> None:
        assert parse_disk_size("1g") == 1024**3
        assert parse_disk_size("1gb") == 1024**3
        assert parse_disk_size("1G") == 1024**3
        assert parse_disk_size("512m") == 512 * 1024**2
        assert parse_disk_size("512M") == 512 * 1024**2

    def test_whitespace_allowed(self) -> None:
        assert parse_disk_size("1 G") == 1024**3
        assert parse_disk_size("512  M") == 512 * 1024**2
        assert parse_disk_size("  2.5 GB  ") == int(2.5 * 1024**3)

    def test_invalid_format_raises(self) -> None:
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("abc")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1X")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("G")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1XB")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1PB")

    def test_negative_size_raises(self) -> None:
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("-1G")


# =========================================================================
# format_disk_size
# =========================================================================


class TestFormatDiskSize:
    """Tests for format_disk_size function."""

    def test_format_bytes(self) -> None:
        assert format_disk_size(512) == "512B"
        assert format_disk_size(1023) == "1023B"

    def test_format_kilobytes(self) -> None:
        assert format_disk_size(1024) == "1K"
        assert format_disk_size(2048) == "2K"
        assert format_disk_size(1536) == "1.5K"

    def test_format_megabytes(self) -> None:
        assert format_disk_size(1024**2) == "1M"
        assert format_disk_size(512 * 1024**2) == "512M"
        assert format_disk_size(int(1.5 * 1024**2)) == "1.5M"

    def test_format_gigabytes(self) -> None:
        assert format_disk_size(1024**3) == "1G"
        assert format_disk_size(2 * 1024**3) == "2G"
        assert format_disk_size(int(1.5 * 1024**3)) == "1.5G"

    def test_format_terabytes(self) -> None:
        assert format_disk_size(1024**4) == "1T"
        assert format_disk_size(2 * 1024**4) == "2T"

    def test_format_zero(self) -> None:
        assert format_disk_size(0) == "0B"

    def test_format_large_numbers(self) -> None:
        # 1.5 TB
        assert format_disk_size(int(1.5 * 1024**4)) == "1.5T"
        # 1024 TB (should still format as TB)
        assert format_disk_size(1024 * 1024**4) == "1024T"


# =========================================================================
# format_sectors_human_readable
# =========================================================================


class TestFormatSectorsHumanReadable:
    def test_small_sectors_mib(self) -> None:
        result = format_sectors_human_readable(2048)  # 2048 * 512 = 1 MiB
        assert "1.0 MiB" in result

    def test_large_sectors_gib(self) -> None:
        result = format_sectors_human_readable(2 * 1024 * 2048)  # 2 GiB
        assert "2.0 GiB" in result


# =========================================================================
# TypeCodeDetector
# =========================================================================


class TestTypeCodeDetector:
    @property
    def detector(self) -> TypeCodeDetector:
        return TypeCodeDetector()

    def test_gpt_root_x86_64(self) -> None:
        partition = {
            "type": "44479540-f297-41b2-9af7-d131d5f0458a",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_gpt_root_aarch64(self) -> None:
        partition = {
            "type": "4f68bce3-e8cd-4db1-96e7-fbcaf984b709",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_mbr_linux(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 0.5

    def test_gpt_esp_exclusion(self) -> None:
        partition = {
            "type": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_gpt_swap_exclusion(self) -> None:
        partition = {
            "type": "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_efi_exclusion(self) -> None:
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_swap_exclusion(self) -> None:
        partition = {
            "type": "82",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_lvm_exclusion(self) -> None:
        partition = {
            "type": "8e",
            "name": "lvm",
            "size": 41943040,
            "fstype": "LVM2_member",
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_mbr_extended_exclusion(self) -> None:
        partition = {
            "type": "85",
            "name": "extended",
            "size": 83886080,
            "fstype": None,
        }
        score = self.detector.score(partition, [partition])
        assert score == -1.0

    def test_unknown_type(self) -> None:
        partition = {
            "type": "unknown-guid-1234",
            "name": "data",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 0.0

    def test_missing_type_field(self) -> None:
        partition = {"name": "data", "size": 41943040, "fstype": "ext4"}
        score = self.detector.score(partition, [partition])
        assert score == 0.0

    def test_case_insensitive_gpt(self) -> None:
        upper = {
            "type": "44479540-F297-41B2-9AF7-D131D5F0458A",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        assert self.detector.score(upper, [upper]) == 1.0


# =========================================================================
# LabelDetector
# =========================================================================


class TestLabelDetector:
    @property
    def detector(self) -> LabelDetector:
        return LabelDetector()

    def test_root_label(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_cloudimg_label(self) -> None:
        partition = {
            "type": "83",
            "name": "cloudimg-rootfs",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_rootfs_label(self) -> None:
        partition = {
            "type": "83",
            "name": "rootfs",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_esp_label_negative(self) -> None:
        partition = {
            "type": "ef",
            "name": "ESP",
            "size": 524288,
            "fstype": "vfat",
        }
        score = self.detector.score(partition, [partition])
        assert score == -0.5

    def test_boot_label_negative(self) -> None:
        partition = {
            "type": "83",
            "name": "boot",
            "size": 1048576,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == -0.5

    def test_swap_label_negative(self) -> None:
        partition = {
            "type": "82",
            "name": "swap",
            "size": 2097152,
            "fstype": "swap",
        }
        score = self.detector.score(partition, [partition])
        assert score == -0.5

    def test_neutral_label(self) -> None:
        partition = {
            "type": "83",
            "name": "data",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 0.0

    def test_missing_name_uses_label_fallback(self) -> None:
        partition = {
            "type": "83",
            "label": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 1.0

    def test_name_takes_precedence_over_label(self) -> None:
        partition = {
            "type": "83",
            "name": "data",
            "label": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 0.0

    def test_case_insensitive_labels(self) -> None:
        partition = {
            "type": "83",
            "name": "ROOT",
            "size": 41943040,
            "fstype": "ext4",
        }
        assert self.detector.score(partition, [partition]) == 1.0


# =========================================================================
# SizeDetector
# =========================================================================


class TestSizeDetector:
    @property
    def detector(self) -> SizeDetector:
        return SizeDetector()

    def test_largest_valid_partition(self) -> None:
        partitions = [
            {"type": "83", "name": "small", "size": 204800, "fstype": "ext4"},
            {"type": "83", "name": "large", "size": 41943040, "fstype": "ext4"},
        ]
        score = self.detector.score(partitions[1], partitions)
        assert score == 0.5

    def test_non_largest_valid_partition(self) -> None:
        partitions = [
            {
                "type": "83",
                "name": "larger",
                "size": 83886080,
                "fstype": "ext4",
            },
            {
                "type": "83",
                "name": "smaller",
                "size": 41943040,
                "fstype": "ext4",
            },
        ]
        score = self.detector.score(partitions[1], partitions)
        assert score == 0.3

    def test_too_small_partition(self) -> None:
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 102400,
            "fstype": "vfat",
        }
        score = self.detector.score(partition, [partition])
        assert score == -0.5

    def test_single_partition_large(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        score = self.detector.score(partition, [partition])
        assert score == 0.5

    def test_single_partition_small(self) -> None:
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 102400,
            "fstype": "vfat",
        }
        score = self.detector.score(partition, [partition])
        assert score == -0.5

    def test_missing_size_treated_as_too_small(self) -> None:
        partition = {"type": "83", "name": "root", "fstype": "ext4"}
        score = self.detector.score(partition, [partition])
        assert score == -0.5


# =========================================================================
# FilesystemDetector
# =========================================================================


class TestFilesystemDetector:
    @property
    def detector(self) -> FilesystemDetector:
        return FilesystemDetector()

    def test_ext4_positive(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "ext4",
        }
        assert self.detector.score(partition, [partition]) == 0.5

    def test_btrfs_positive(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "btrfs",
        }
        assert self.detector.score(partition, [partition]) == 0.5

    def test_xfs_positive(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "xfs",
        }
        assert self.detector.score(partition, [partition]) == 0.5

    def test_f2fs_positive(self) -> None:
        partition = {
            "type": "83",
            "name": "root",
            "size": 41943040,
            "fstype": "f2fs",
        }
        assert self.detector.score(partition, [partition]) == 0.5

    def test_vfat_negative(self) -> None:
        partition = {
            "type": "ef",
            "name": "EFI",
            "size": 524288,
            "fstype": "vfat",
        }
        assert self.detector.score(partition, [partition]) == -0.8

    def test_missing_fstype(self) -> None:
        partition = {"type": "83", "name": "root", "size": 41943040}
        assert self.detector.score(partition, [partition]) == 0.0


# =========================================================================
# RootPartitionDetector — integration
# =========================================================================


class TestRootPartitionDetector:
    def test_single_partition_auto_select(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {"type": "83", "name": "root", "size": 41943040, "fstype": "ext4"},
        ]
        assert detector.detect(partitions) == 1

    def test_clear_winner(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {"type": "ef", "name": "EFI", "size": 524288, "fstype": "vfat"},
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "root",
                "size": 41943040,
                "fstype": "ext4",
            },
            {"type": "82", "name": "swap", "size": 2097152, "fstype": "swap"},
        ]
        assert detector.detect(partitions) == 2

    def test_tie_detection_raises_error(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {"type": "83", "name": "root1", "size": 41943040, "fstype": "ext4"},
            {"type": "83", "name": "root2", "size": 41943040, "fstype": "ext4"},
        ]
        with pytest.raises(TieDetectedError):
            detector.detect(partitions)

    def test_all_negative_scores_raises_error(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {"type": "ef", "name": "EFI", "size": 524288, "fstype": "vfat"},
            {"type": "82", "name": "swap", "size": 2097152, "fstype": "swap"},
        ]
        with pytest.raises(RootPartitionDetectionError):
            detector.detect(partitions)

    def test_empty_partitions_raises_error(self) -> None:
        detector = RootPartitionDetector()
        with pytest.raises(ValueError):
            detector.detect([])

    def test_realistic_ubuntu_image(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {
                "type": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b",
                "name": "EFI System Partition",
                "size": 1048576,
                "fstype": "vfat",
            },
            {
                "type": "44479540-f297-41b2-9af7-d131d5f0458a",
                "name": "cloudimg-rootfs",
                "size": 83886080,
                "fstype": "ext4",
            },
        ]
        assert detector.detect(partitions) == 2

    def test_disabled_detectors(self) -> None:
        detector = RootPartitionDetector(disabled_detectors=["label"])
        partitions = [
            {"type": "83", "name": "data", "size": 41943040, "fstype": "ext4"},
        ]
        assert detector.detect(partitions) == 1

    def test_none_values_in_partition(self) -> None:
        detector = RootPartitionDetector()
        partitions = [
            {"type": None, "name": None, "size": None, "fstype": None}
        ]
        assert detector.detect(partitions) == 1
