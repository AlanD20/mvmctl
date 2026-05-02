"""Tests for LogService — stateless log file operations."""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.core.logs._service import LogService
from mvmctl.exceptions import ConfigError, MVMError, VMNotFoundError


class TestGetLogPath:
    """Tests for LogService.get_log_path()."""

    def test_get_log_path_boot(self, tmp_path: Path) -> None:
        """get_log_path returns firecracker.console.log for boot type."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        log_file = vm_dir / "firecracker.console.log"
        log_file.write_text("boot output\n")

        with (
            patch_get_vm_dir(vm_dir),
        ):
            result = LogService.get_log_path(
                "a" * 64,
                "boot",
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert result == log_file

    def test_get_log_path_os(self, tmp_path: Path) -> None:
        """get_log_path returns firecracker.log for os type."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()
        log_file = vm_dir / "firecracker.log"
        log_file.write_text("os log\n")

        with patch_get_vm_dir(vm_dir):
            result = LogService.get_log_path(
                "a" * 64,
                "os",
                log_filename="firecracker.log",
                serial_output_filename="firecracker.console.log",
            )
        assert result == log_file

    def test_get_log_path_unknown_type(self, tmp_path: Path) -> None:
        """get_log_path raises ConfigError for unknown log type."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        with patch_get_vm_dir(vm_dir):
            with pytest.raises(ConfigError, match="Unknown log type"):
                LogService.get_log_path(
                    "a" * 64,
                    "unknown",
                    log_filename="firecracker.log",
                    serial_output_filename="firecracker.console.log",
                )

    def test_get_log_path_missing_vm(self, tmp_path: Path) -> None:
        """get_log_path raises VMNotFoundError when VM directory missing."""
        nonexistent = tmp_path / "no-such-vm"

        with patch_get_vm_dir(nonexistent):
            with pytest.raises(VMNotFoundError, match="not found"):
                LogService.get_log_path(
                    "b" * 64,
                    "boot",
                    log_filename="firecracker.log",
                    serial_output_filename="firecracker.console.log",
                )

    def test_get_log_path_missing_file(self, tmp_path: Path) -> None:
        """get_log_path raises VMNotFoundError when log file is missing."""
        vm_dir = tmp_path / "test-vm"
        vm_dir.mkdir()

        with patch_get_vm_dir(vm_dir):
            with pytest.raises(VMNotFoundError, match="Log file not found"):
                LogService.get_log_path(
                    "a" * 64,
                    "boot",
                    log_filename="firecracker.log",
                    serial_output_filename="firecracker.console.log",
                )


class TestReadLogLines:
    """Tests for LogService.read_log_lines()."""

    def test_read_log_lines_basic(self, tmp_path: Path) -> None:
        """read_log_lines returns last N lines."""
        log_file = tmp_path / "test.log"
        log_file.write_text("".join(f"line {i}\n" for i in range(100)))

        result = LogService.read_log_lines(log_file, lines=10)
        assert len(result) == 10
        assert result[0] == "line 90"
        assert result[-1] == "line 99"

    def test_read_log_lines_fewer_than_requested(self, tmp_path: Path) -> None:
        """read_log_lines returns all lines when fewer exist than requested."""
        log_file = tmp_path / "test.log"
        log_file.write_text("line 0\nline 1\nline 2\n")

        result = LogService.read_log_lines(log_file, lines=50)
        assert len(result) == 3
        assert result[0] == "line 0"
        assert result[-1] == "line 2"

    def test_read_log_lines_empty_file(self, tmp_path: Path) -> None:
        """read_log_lines returns empty list for empty file."""
        log_file = tmp_path / "empty.log"
        log_file.write_text("")

        result = LogService.read_log_lines(log_file, lines=10)
        assert result == []

    def test_read_log_lines_single_line(self, tmp_path: Path) -> None:
        """read_log_lines works with single-line file."""
        log_file = tmp_path / "single.log"
        log_file.write_text("only line\n")

        result = LogService.read_log_lines(log_file, lines=10)
        assert len(result) == 1
        assert result[0] == "only line"

    def test_read_log_lines_io_error(self, tmp_path: Path) -> None:
        """read_log_lines raises MVMError on I/O error."""
        log_file = tmp_path / "missing.log"
        with pytest.raises(MVMError, match="Error reading log file"):
            LogService.read_log_lines(log_file, lines=10)


class TestFollowLog:
    """Tests for LogService.follow_log()."""

    def test_follow_log_io_error(self, tmp_path: Path) -> None:
        """follow_log raises MVMError on non-existent file."""
        log_file = tmp_path / "nonexistent.log"
        gen = LogService.follow_log(log_file)
        with pytest.raises(MVMError, match="Error following log"):
            list(gen)


def patch_get_vm_dir(return_value: Path):
    """Helper to patch CacheUtils.get_vm_dir."""
    from unittest.mock import patch

    return patch(
        "mvmctl.utils.common.CacheUtils.get_vm_dir", return_value=return_value
    )
