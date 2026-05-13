"""Tests for LoopMountManager — binary resolution, payload building, subprocess execution."""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared._loopmount._manager import (
    LoopMountManager,
)
from mvmctl.exceptions import (
    LoopMountError,
    LoopMountTimeoutError,
    ProcessError,
)
from mvmctl.utils.common import CacheUtils


class TestResolveBinaryPath:
    def test_binary_found(self):
        """_resolve_binary_path returns Path when binary exists in bin dir."""
        bin_dir = CacheUtils.get_bin_dir()
        bin_dir.mkdir(parents=True, exist_ok=True)
        binary_path = bin_dir / "mvm-provision"
        binary_path.write_text("fake binary")
        binary_path.chmod(0o755)

        result = LoopMountManager._resolve_binary_path()
        assert result is not None
        assert result == binary_path

    def test_binary_not_found(self):
        """_resolve_binary_path returns None when binary does not exist."""
        # Ensure the bin dir is empty
        bin_dir = CacheUtils.get_bin_dir()
        if bin_dir.exists():
            for f in bin_dir.iterdir():
                f.unlink()
        else:
            bin_dir.mkdir(parents=True, exist_ok=True)

        result = LoopMountManager._resolve_binary_path()
        assert result is None


class TestBuildOps:
    def test_empty(self):
        """_build_ops returns minimal payload with empty operations dict."""
        payload = LoopMountManager._build_ops(image_path="/img.ext4")
        assert payload == {"image": "/img.ext4", "operations": {}}

    def test_with_files(self):
        payload = LoopMountManager._build_ops(
            image_path="/img.ext4",
            fs_type="ext4",
            files=[
                {"path": "/etc/hostname", "data": "dGVzdA==", "mode": 0o644}
            ],
        )
        assert payload["image"] == "/img.ext4"
        assert payload["fs_type"] == "ext4"
        assert payload["operations"]["files"] == [
            {"path": "/etc/hostname", "data": "dGVzdA==", "mode": 0o644}
        ]

    def test_with_commands(self):
        payload = LoopMountManager._build_ops(
            image_path="/img.ext4",
            commands=["ssh-keygen -A", "systemctl enable sshd"],
        )
        assert payload["operations"]["commands"] == [
            "ssh-keygen -A",
            "systemctl enable sshd",
        ]

    def test_with_copy_dirs(self):
        payload = LoopMountManager._build_ops(
            image_path="/img.ext4",
            copy_dirs=[{"src": "/src", "dst": "/dst"}],
        )
        assert payload["operations"]["copy_dirs"] == [
            {"src": "/src", "dst": "/dst"}
        ]

    def test_with_resize(self):
        payload = LoopMountManager._build_ops(
            image_path="/img.ext4",
            resize={"action": "grow", "bytes": 8589934592},
        )
        assert payload["operations"]["resize"] == {
            "action": "grow",
            "bytes": 8589934592,
        }

    def test_all_operation_types(self):
        payload = LoopMountManager._build_ops(
            image_path="/img.ext4",
            fs_type="ext4",
            files=[{"path": "/a", "data": "YQ==", "mode": 0o644}],
            commands=["cmd1"],
            copy_dirs=[{"src": "/s", "dst": "/d"}],
            resize={"action": "grow", "bytes": 1000},
        )
        ops = payload["operations"]
        assert "files" in ops
        assert "commands" in ops
        assert "copy_dirs" in ops
        assert "resize" in ops


class TestIsBinaryAvailable:
    def test_available(self):
        bin_dir = CacheUtils.get_bin_dir()
        bin_dir.mkdir(parents=True, exist_ok=True)
        binary_path = bin_dir / "mvm-provision"
        binary_path.write_text("fake")
        binary_path.chmod(0o755)

        assert LoopMountManager.is_binary_available() is True

    def test_not_available(self, mocker):
        bin_dir = CacheUtils.get_bin_dir()
        if bin_dir.exists():
            for f in bin_dir.iterdir():
                if f.name == "mvm-provision":
                    f.unlink()
        else:
            bin_dir.mkdir(parents=True, exist_ok=True)

        # _DEV_PROCESS_PATH points to source code, which always exists in dev.
        # Replace the entire _DEV_PROCESS_PATH with a mock that returns False for exists().
        mock_path = mocker.MagicMock()
        mock_path.exists.return_value = False
        mocker.patch(
            "mvmctl.core._shared._loopmount._manager._DEV_PROCESS_PATH",
            mock_path,
        )
        assert LoopMountManager.is_binary_available() is False


class TestExtractError:
    def test_extract_error_from_stderr(self):
        proc = MagicMock()
        proc.returncode = 1
        proc.stderr = "some error message"
        proc.stdout = "{}"
        result = LoopMountManager._extract_error(proc)
        assert "some error message" in result

    def test_extract_error_from_stdout_json(self):
        proc = MagicMock()
        proc.returncode = 1
        proc.stderr = b""
        error_response = json.dumps(
            {"status": "error", "error": "mount failed"}
        ).encode()
        proc.stdout = error_response
        result = LoopMountManager._extract_error(proc)
        assert "mount failed" in result

    def test_extract_error_fallback_to_exit_code(self):
        proc = MagicMock()
        proc.returncode = 42
        proc.stderr = b""
        proc.stdout = b"not json"
        result = LoopMountManager._extract_error(proc)
        assert "42" in result


class TestExecute:
    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_success_binary_path(self, mock_run, mock_resolve):
        """execute() uses compiled binary when available."""
        mock_resolve.return_value = Path("/usr/bin/mvm-provision")
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({"status": "ok"}),
            stderr="",
        )
        result = LoopMountManager.execute(image_path="/img.ext4")
        assert result == {"status": "ok"}
        # Verify the command includes the binary path
        cmd = mock_run.call_args[0][0]
        assert "sudo" in cmd
        assert "/usr/bin/mvm-provision" in str(cmd)

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_timeout(self, mock_run, mock_resolve):
        """execute() raises LoopMountTimeoutError on subprocess timeout."""
        mock_resolve.return_value = None  # dev mode
        mock_run.side_effect = ProcessError("Command timed out after 60s")

        with pytest.raises(LoopMountTimeoutError, match="timed out"):
            LoopMountManager.execute(image_path="/img.ext4", timeout=60)

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_nonzero_returncode(self, mock_run, mock_resolve):
        """execute() raises LoopMountError on non-zero exit."""
        mock_resolve.return_value = None
        mock_run.return_value = MagicMock(
            returncode=1,
            stdout="{}",
            stderr="mount failed",
        )
        with pytest.raises(LoopMountError, match="mount failed"):
            LoopMountManager.execute(image_path="/img.ext4")

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_invalid_json_response(self, mock_run, mock_resolve):
        """execute() raises LoopMountError when response is not valid JSON."""
        mock_resolve.return_value = None
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="not valid json",
            stderr="",
        )
        with pytest.raises(LoopMountError, match="Failed to parse"):
            LoopMountManager.execute(image_path="/img.ext4")

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_response_not_dict(self, mock_run, mock_resolve):
        """execute() raises LoopMountError when response is not a dict."""
        mock_resolve.return_value = None
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout='["not", "a", "dict"]',
            stderr="",
        )
        with pytest.raises(LoopMountError, match="not a dict"):
            LoopMountManager.execute(image_path="/img.ext4")

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_error_status_response(self, mock_run, mock_resolve):
        """execute() raises LoopMountError when response has error status."""
        mock_resolve.return_value = None
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps(
                {
                    "status": "error",
                    "error": "filesystem not found",
                    "step": "mount",
                }
            ),
            stderr="",
        )
        with pytest.raises(LoopMountError, match="filesystem not found"):
            LoopMountManager.execute(image_path="/img.ext4")

    @patch(
        "mvmctl.core._shared._loopmount._manager.LoopMountManager._resolve_binary_path"
    )
    @patch("mvmctl.core._shared._loopmount._manager.run_cmd")
    def test_with_all_operation_types(self, mock_run, mock_resolve):
        """execute() builds correct payload with all operation types."""
        mock_resolve.return_value = None
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=json.dumps({"status": "ok"}),
            stderr="",
        )
        LoopMountManager.execute(
            image_path="/img.ext4",
            fs_type="ext4",
            files=[{"path": "/a", "data": "YQ==", "mode": 0o644}],
            commands=["cmd1"],
            copy_dirs=[{"src": "/s", "dst": "/d"}],
            resize={"action": "grow", "bytes": 1000},
        )
        # Verify the payload sent to stdin
        input_data = json.loads(mock_run.call_args[1]["input"])
        assert input_data["image"] == "/img.ext4"
        assert input_data["fs_type"] == "ext4"
        ops = input_data["operations"]
        assert "files" in ops
        assert "commands" in ops
        assert "copy_dirs" in ops
        assert "resize" in ops
