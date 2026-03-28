"""Tests for cloud_init_status module."""

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.cloud_init_status import (
    check_cloud_init_status,
    wait_for_cloud_init_done,
)
from mvmctl.models.vm import CloudInitStatus


class TestCheckCloudInitStatus:
    """Tests for check_cloud_init_status function."""

    def test_pending_when_file_not_exists(self, tmp_path: Path) -> None:
        """Returns PENDING when console log doesn't exist."""
        log_path = tmp_path / "nonexistent.log"
        result = check_cloud_init_status("test-vm", log_path)
        assert result == CloudInitStatus.PENDING

    def test_running_when_no_marker(self, tmp_path: Path) -> None:
        """Returns RUNNING when file exists but no done marker."""
        log_path = tmp_path / "console.log"
        log_path.write_text("Booting kernel...\nStarting services...")
        result = check_cloud_init_status("test-vm", log_path)
        assert result == CloudInitStatus.RUNNING

    def test_done_when_marker_found(self, tmp_path: Path) -> None:
        """Returns DONE when final_message marker is found."""
        log_path = tmp_path / "console.log"
        log_path.write_text(
            "[  123.456] cloud-init[1234]: Running module final-message\n"
            "[  124.789] cloud-init[1234]: [CLOUDINIT] final_message: 'mvm cloud-init done'\n"
        )
        result = check_cloud_init_status("test-vm", log_path)
        assert result == CloudInitStatus.DONE

    def test_running_with_io_error(self, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
        """Returns RUNNING when file read fails."""
        log_path = tmp_path / "console.log"
        log_path.write_text("some content")

        def raise_io_error(*args: object, **kwargs: object) -> str:
            raise IOError("Permission denied")

        monkeypatch.setattr(Path, "read_text", raise_io_error)
        result = check_cloud_init_status("test-vm", log_path)
        assert result == CloudInitStatus.RUNNING


class TestWaitForCloudInitDone:
    """Tests for wait_for_cloud_init_done function."""

    def test_immediate_done(self, tmp_path: Path) -> None:
        """Returns True immediately when done marker exists."""
        log_path = tmp_path / "console.log"
        log_path.write_text("final_message: 'mvm cloud-init done'\n")

        result = wait_for_cloud_init_done("test-vm", log_path, timeout=5)
        assert result is True

    def test_timeout_when_not_done(self, tmp_path: Path) -> None:
        """Returns False when timeout expires."""
        log_path = tmp_path / "console.log"
        log_path.write_text("Still booting...\n")

        with patch("mvmctl.core.cloud_init_status.CONST_CLOUD_INIT_POLL_INTERVAL_S", 0.1):
            result = wait_for_cloud_init_done("test-vm", log_path, timeout=0.5)
        assert result is False

    def test_waits_until_done(self, tmp_path: Path) -> None:
        """Returns True once done marker appears."""
        log_path = tmp_path / "console.log"
        original_content = "Still booting...\n"
        log_path.write_text(original_content)

        # First two reads return no marker, third returns with marker
        call_count = 0

        def mock_read(*args: object, **kwargs: object) -> str:
            nonlocal call_count
            call_count += 1
            if call_count > 2:
                return "final_message: 'mvm cloud-init done'\n"
            return "Still booting...\n"

        with patch.object(Path, "read_text", mock_read):
            with patch("mvmctl.core.cloud_init_status.CONST_CLOUD_INIT_POLL_INTERVAL_S", 0.1):
                result = wait_for_cloud_init_done("test-vm", log_path, timeout=5)
        assert result is True
