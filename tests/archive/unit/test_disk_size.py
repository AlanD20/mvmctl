"""Tests for disk size parsing utilities."""

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.disk_size import format_disk_size, parse_disk_size


class TestParseDiskSize:
    """Tests for parse_disk_size function."""

    def test_parse_bytes(self) -> None:
        """Test parsing bytes."""
        assert parse_disk_size("512B") == 512
        assert parse_disk_size("1024") == 1024  # Default to bytes
        assert parse_disk_size("0B") == 0

    def test_parse_kilobytes(self) -> None:
        """Test parsing kilobytes."""
        assert parse_disk_size("1K") == 1024
        assert parse_disk_size("1KB") == 1024
        assert parse_disk_size("512K") == 512 * 1024
        assert parse_disk_size("2.5KB") == int(2.5 * 1024)

    def test_parse_megabytes(self) -> None:
        """Test parsing megabytes."""
        assert parse_disk_size("1M") == 1024**2
        assert parse_disk_size("1MB") == 1024**2
        assert parse_disk_size("512M") == 512 * 1024**2
        assert parse_disk_size("2.5MB") == int(2.5 * 1024**2)

    def test_parse_gigabytes(self) -> None:
        """Test parsing gigabytes."""
        assert parse_disk_size("1G") == 1024**3
        assert parse_disk_size("1GB") == 1024**3
        assert parse_disk_size("2G") == 2 * 1024**3
        assert parse_disk_size("2.5GB") == int(2.5 * 1024**3)

    def test_parse_terabytes(self) -> None:
        """Test parsing terabytes."""
        assert parse_disk_size("1T") == 1024**4
        assert parse_disk_size("1TB") == 1024**4
        assert parse_disk_size("2T") == 2 * 1024**4

    def test_case_insensitive(self) -> None:
        """Test that parsing is case-insensitive."""
        assert parse_disk_size("1g") == 1024**3
        assert parse_disk_size("1gb") == 1024**3
        assert parse_disk_size("1G") == 1024**3
        assert parse_disk_size("1GB") == 1024**3
        assert parse_disk_size("512m") == 512 * 1024**2
        assert parse_disk_size("512M") == 512 * 1024**2
        assert parse_disk_size("512mb") == 512 * 1024**2
        assert parse_disk_size("512MB") == 512 * 1024**2

    def test_whitespace_allowed(self) -> None:
        """Test that whitespace is allowed between number and unit."""
        assert parse_disk_size("1 G") == 1024**3
        assert parse_disk_size("512  M") == 512 * 1024**2
        assert parse_disk_size("  2.5 GB  ") == int(2.5 * 1024**3)

    def test_invalid_format_raises(self) -> None:
        """Test that invalid formats raise MVMError."""
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("abc")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1X")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("G")
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("")
        # Units not in the allowed set should also raise format error
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1XB")  # X is not a valid unit prefix
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("1PB")  # Petabytes not supported

    def test_negative_size_raises(self) -> None:
        """Test that negative sizes raise MVMError."""
        # Negative numbers don't match the regex (which requires digits only)
        with pytest.raises(MVMError, match="Invalid disk size format"):
            parse_disk_size("-1G")


class TestFormatDiskSize:
    """Tests for format_disk_size function."""

    def test_format_bytes(self) -> None:
        """Test formatting bytes."""
        assert format_disk_size(512) == "512B"
        assert format_disk_size(1023) == "1023B"

    def test_format_kilobytes(self) -> None:
        """Test formatting kilobytes."""
        assert format_disk_size(1024) == "1K"
        assert format_disk_size(2048) == "2K"
        assert format_disk_size(1536) == "1.5K"

    def test_format_megabytes(self) -> None:
        """Test formatting megabytes."""
        assert format_disk_size(1024**2) == "1M"
        assert format_disk_size(512 * 1024**2) == "512M"
        assert format_disk_size(int(1.5 * 1024**2)) == "1.5M"

    def test_format_gigabytes(self) -> None:
        """Test formatting gigabytes."""
        assert format_disk_size(1024**3) == "1G"
        assert format_disk_size(2 * 1024**3) == "2G"
        assert format_disk_size(int(1.5 * 1024**3)) == "1.5G"

    def test_format_terabytes(self) -> None:
        """Test formatting terabytes."""
        assert format_disk_size(1024**4) == "1T"
        assert format_disk_size(2 * 1024**4) == "2T"

    def test_format_zero(self) -> None:
        """Test formatting zero bytes."""
        assert format_disk_size(0) == "0B"

    def test_format_large_numbers(self) -> None:
        """Test formatting large numbers."""
        # 1.5 TB
        assert format_disk_size(int(1.5 * 1024**4)) == "1.5T"
        # 1024 TB (should still format as TB)
        assert format_disk_size(1024 * 1024**4) == "1024T"
