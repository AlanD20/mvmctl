"""Tests for utils/_system.py — ProcessRunner, subprocess, signal handling."""

from __future__ import annotations

import signal
import subprocess
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import ProcessError
from mvmctl.utils._system import (
    SigtermContext,
    has_python_ancestor,
    is_process_running,
    privileged_cmd,
    require_mvm_group_membership,
    run_cmd,
    sigterm_context,
    stream_cmd,
)

# ---------------------------------------------------------------------------
# run_cmd
# ---------------------------------------------------------------------------


class TestRunCmd:
    """Tests for run_cmd()."""

    def test_success(self):
        result = run_cmd(["echo", "hello"])
        assert result.returncode == 0
        assert result.stdout.strip() == "hello"

    def test_returns_completed_process(self):
        result = run_cmd(["true"])
        assert isinstance(result, subprocess.CompletedProcess)
        assert result.returncode == 0

    def test_command_not_found(self):
        with pytest.raises(ProcessError, match="Command not found"):
            run_cmd(["nonexistent_binary_xyz_12345"])

    def test_command_fails(self):
        with pytest.raises(ProcessError, match="exit 1"):
            run_cmd(["false"])

    def test_failure_includes_stderr(self):
        with pytest.raises(ProcessError, match="(?i)no such file"):
            run_cmd(["ls", "/nonexistent_path_xyz_12345"])

    def test_with_cwd(self, tmp_path: Path):
        result = run_cmd(["pwd"], cwd=str(tmp_path))
        assert result.stdout.strip() == str(tmp_path.resolve())

    def test_check_false_returns_on_failure(self):
        result = run_cmd(["false"], check=False)
        assert result.returncode != 0

    def test_check_false_no_exception(self):
        result = run_cmd(["ls", "/nonexistent_path_xyz_12345"], check=False)
        assert result.returncode != 0

    def test_captures_stdout(self):
        result = run_cmd(["printf", "line1\nline2\n"])
        assert "line1" in result.stdout
        assert "line2" in result.stdout

    def test_captures_stderr_on_failure(self):
        with pytest.raises(ProcessError) as exc_info:
            run_cmd(["ls", "/nonexistent_path_xyz_12345"])
        error_str = str(exc_info.value)
        assert "ls" in error_str
        assert "exit" in error_str
        assert "/nonexistent_path_xyz_12345" not in error_str.split("\n")[0]

    @patch("mvmctl.utils._system.subprocess.run")
    def test_file_not_found_mocked(self, mock_run):
        mock_run.side_effect = FileNotFoundError("no such binary")
        with pytest.raises(ProcessError, match="Command not found: badcmd"):
            run_cmd(["badcmd", "--arg"])

    @patch("mvmctl.utils._system.subprocess.run")
    def test_called_process_error_mocked(self, mock_run):
        err = subprocess.CalledProcessError(2, "mycmd")
        err.stderr = "some error detail"
        mock_run.side_effect = err
        with pytest.raises(ProcessError, match="exit 2.*mycmd"):
            run_cmd(["mycmd"])

    @patch("mvmctl.utils._system.subprocess.run")
    def test_called_process_error_no_stderr(self, mock_run):
        err = subprocess.CalledProcessError(1, "mycmd")
        err.stderr = ""
        mock_run.side_effect = err
        with pytest.raises(ProcessError, match="exit 1"):
            run_cmd(["mycmd"])

    @patch("mvmctl.utils._system.subprocess.run")
    def test_called_process_error_none_stderr(self, mock_run):
        err = subprocess.CalledProcessError(1, "mycmd")
        err.stderr = None
        mock_run.side_effect = err
        with pytest.raises(ProcessError, match="exit 1"):
            run_cmd(["mycmd"])

    @patch("mvmctl.utils._system.subprocess.run")
    def test_sanitized_error_message(self, mock_run):
        """Error messages should only show command name, not full arguments."""
        err = subprocess.CalledProcessError(1, "mycmd")
        err.stderr = "sensitive error details"
        mock_run.side_effect = err
        with pytest.raises(ProcessError) as exc_info:
            run_cmd(["mycmd", "--secret-arg", "password123"])
        error_str = str(exc_info.value)
        assert "mycmd" in error_str
        assert "--secret-arg" not in error_str
        assert "password123" not in error_str

    @patch("mvmctl.utils._system.subprocess.run")
    def test_sanitized_stderr_truncated(self, mock_run):
        """Stderr should be limited to 100 characters in error messages."""
        err = subprocess.CalledProcessError(1, "mycmd")
        err.stderr = "x" * 200
        mock_run.side_effect = err
        with pytest.raises(ProcessError) as exc_info:
            run_cmd(["mycmd"])
        error_str = str(exc_info.value)
        assert "..." in error_str
        assert len(error_str) < 250

    @patch("mvmctl.utils._system.subprocess.run")
    def test_passes_cwd(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0)
        run_cmd(["ls"], cwd="/tmp")
        mock_run.assert_called_once_with(
            ["ls"],
            capture_output=True,
            text=True,
            check=True,
            cwd="/tmp",
        )

    @patch("mvmctl.utils._system.subprocess.run")
    def test_capture_false(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0)
        run_cmd(["ls"], capture=False)
        mock_run.assert_called_once_with(
            ["ls"],
            capture_output=False,
            text=True,
            check=True,
            cwd=None,
        )


# ---------------------------------------------------------------------------
# stream_cmd
# ---------------------------------------------------------------------------


class TestStreamCmd:
    """Tests for stream_cmd()."""

    def test_yields_lines(self):
        lines = list(stream_cmd(["printf", "line1\nline2\nline3\n"]))
        assert lines == ["line1", "line2", "line3"]

    def test_strips_trailing_newlines(self):
        lines = list(stream_cmd(["echo", "hello"]))
        assert lines == ["hello"]
        assert not any(line.endswith("\n") for line in lines)

    def test_command_not_found(self):
        with pytest.raises(ProcessError, match="Command not found"):
            list(stream_cmd(["nonexistent_binary_xyz_12345"]))

    def test_failure_raises_after_output(self):
        with pytest.raises(ProcessError, match="exit"):
            list(stream_cmd(["bash", "-c", "echo output && exit 42"]))

    def test_empty_output(self):
        lines = list(stream_cmd(["true"]))
        assert lines == []

    def test_with_cwd(self, tmp_path: Path):
        lines = list(stream_cmd(["pwd"], cwd=str(tmp_path)))
        assert lines == [str(tmp_path.resolve())]

    def test_stderr_merged_to_stdout(self):
        lines = list(stream_cmd(["bash", "-c", "echo out && echo err >&2"]))
        assert "out" in lines
        assert "err" in lines

    @patch("mvmctl.utils._system.subprocess.Popen")
    def test_file_not_found_mocked(self, mock_popen):
        mock_popen.side_effect = FileNotFoundError("no such binary")
        with pytest.raises(ProcessError, match="Command not found: badcmd"):
            list(stream_cmd(["badcmd"]))

    @staticmethod
    def _make_mock_proc(lines, wait_return=0):
        """Create a mock subprocess.Popen return value with IO-like stdout."""
        mock_proc = MagicMock()
        # stdout must be an IO stream with close() and __iter__ support
        stdout_mock = MagicMock()
        stdout_mock.__iter__.return_value = iter(lines)
        stdout_mock.close = MagicMock()
        mock_proc.stdout = stdout_mock
        mock_proc.wait.return_value = wait_return
        return mock_proc

    @patch("mvmctl.utils._system.subprocess.Popen")
    def test_non_zero_exit_mocked(self, mock_popen):
        mock_proc = self._make_mock_proc(
            ["line1\n", "line2\n"], wait_return=5
        )
        mock_popen.return_value = mock_proc
        with pytest.raises(ProcessError, match="exit 5"):
            list(stream_cmd(["failcmd"]))

    @patch("mvmctl.utils._system.subprocess.Popen")
    def test_success_mocked(self, mock_popen):
        mock_proc = self._make_mock_proc(["hello\n", "world\n"])
        mock_popen.return_value = mock_proc
        lines = list(stream_cmd(["mycmd"]))
        assert lines == ["hello", "world"]

    @patch("mvmctl.utils._system.subprocess.Popen")
    def test_passes_cwd_mocked(self, mock_popen):
        mock_proc = self._make_mock_proc([])
        mock_popen.return_value = mock_proc
        list(stream_cmd(["ls"], cwd="/tmp"))
        mock_popen.assert_called_once_with(
            ["ls"],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            cwd="/tmp",
        )


# ---------------------------------------------------------------------------
# privileged_cmd
# ---------------------------------------------------------------------------


class TestPrivilegedCmd:
    """Tests for privileged_cmd()."""

    @patch("mvmctl.utils._system.os.getuid", return_value=0)
    def test_as_root_returns_unchanged(self, mock_getuid):
        result = privileged_cmd(["ip", "link"])
        assert result == ["ip", "link"]

    @patch("mvmctl.utils._system.require_mvm_group_membership")
    @patch("mvmctl.utils._system.os.getuid", return_value=1000)
    def test_as_non_root_prepends_sudo(self, mock_getuid, mock_require):
        result = privileged_cmd(["ip", "link"])
        assert result == ["sudo", "ip", "link"]

    @patch("mvmctl.utils._system.require_mvm_group_membership")
    @patch("mvmctl.utils._system.os.getuid", return_value=1000)
    def test_as_non_root_requires_group(
        self, mock_getuid, mock_require
    ):
        from mvmctl.exceptions import PrivilegeError

        mock_require.side_effect = PrivilegeError("Not in mvm group")
        with pytest.raises(PrivilegeError):
            privileged_cmd(["ip", "link"])


# ---------------------------------------------------------------------------
# SigtermContext
# ---------------------------------------------------------------------------


class TestSigtermContext:
    """Tests for SigtermContext and sigterm_context()."""

    def test_sets_and_restores_signal_handler(self):
        cleanup_called = False

        def cleanup():
            nonlocal cleanup_called
            cleanup_called = True

        # Capture old handler BEFORE entering context
        old = signal.getsignal(signal.SIGTERM)
        ctx = SigtermContext(cleanup)
        ctx.__enter__()
        # Simulate SIGTERM
        ctx._handle_signal(signal.SIGTERM, None)
        assert cleanup_called
        ctx.__exit__(None, None, None)
        # Handler should be restored to the pre-enter handler
        assert signal.getsignal(signal.SIGTERM) is old

    def test_context_manager_decorator(self):
        cleanup_called = False

        def cleanup():
            nonlocal cleanup_called
            cleanup_called = True

        with sigterm_context(cleanup):
            pass
        # Cleanup should not be called since no signal was sent
        assert not cleanup_called


# ---------------------------------------------------------------------------
# is_process_running
# ---------------------------------------------------------------------------


class TestIsProcessRunning:
    """Tests for is_process_running()."""

    @patch("mvmctl.utils._system.os.kill")
    def test_running_process(self, mock_kill):
        mock_kill.return_value = None
        assert is_process_running(1234) is True

    @patch("mvmctl.utils._system.os.kill", side_effect=ProcessLookupError)
    def test_not_running(self, mock_kill):
        assert is_process_running(99999) is False

    def test_none_pid(self):
        assert is_process_running(None) is False

    @patch("mvmctl.utils._system.os.kill", side_effect=OSError)
    def test_oserror_not_running(self, mock_kill):
        assert is_process_running(1234) is False


# ---------------------------------------------------------------------------
# has_python_ancestor
# ---------------------------------------------------------------------------


class TestHasPythonAncestor:
    """Tests for has_python_ancestor()."""

    @patch(
        "mvmctl.utils._system.open",
        side_effect=[
            # First call: cmdline with python
            MagicMock(
                __enter__=MagicMock(
                    return_value=MagicMock(
                        read=MagicMock(return_value=b"python3 /usr/bin/mvmctl")
                    )
                )
            ),
            # Second call not reached
        ],
    )
    def test_finds_python_ancestor(self, mock_open):
        import builtins

        with patch.object(builtins, "open", mock_open):
            result = has_python_ancestor(1234)
            assert result is True

    def test_nonexistent_pid(self):
        result = has_python_ancestor(99999999)
        assert result is False


# ---------------------------------------------------------------------------
# require_mvm_group_membership
# ---------------------------------------------------------------------------


class TestRequireMvmGroupMembership:
    """Tests for require_mvm_group_membership()."""

    @patch("grp.getgrnam", side_effect=KeyError("mvm"))
    @patch("pwd.getpwnam")
    def test_missing_group_raises(self, mock_pwd_unused, mock_grp_unused):
        from mvmctl.exceptions import PrivilegeError

        with pytest.raises(PrivilegeError, match="Group.*does not exist"):
            require_mvm_group_membership()
