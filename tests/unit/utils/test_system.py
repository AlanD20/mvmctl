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
            timeout=None,
            input=None,
            env=None,
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
            timeout=None,
            input=None,
            env=None,
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
        mock_proc = self._make_mock_proc(["line1\n", "line2\n"], wait_return=5)
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
# run_cmd — privileged=True
# ---------------------------------------------------------------------------


class TestRunCmdPrivileged:
    """Tests for run_cmd with privileged=True."""

    @patch("mvmctl.utils._system.subprocess.run")
    @patch("mvmctl.utils._system.os.getuid", return_value=0)
    def test_as_root_no_sudo(self, mock_getuid, mock_run):
        """When running as root, privileged=True does not prepend sudo."""
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        run_cmd(["ip", "link"], privileged=True)
        mock_run.assert_called_once()
        args = mock_run.call_args[0][0]
        assert args == ["ip", "link"]

    @patch("mvmctl.utils._system.subprocess.run")
    @patch("mvmctl.utils._system.require_mvm_group_membership")
    @patch("mvmctl.utils._system.os.getuid", return_value=1000)
    def test_as_non_root_prepends_sudo(
        self, mock_getuid, mock_require, mock_run
    ):
        """When not root, privileged=True prepends sudo and checks group."""
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        run_cmd(["ip", "link"], privileged=True)
        mock_require.assert_called_once()
        mock_run.assert_called_once()
        args = mock_run.call_args[0][0]
        assert args == ["sudo", "ip", "link"]

    @patch("mvmctl.utils._system.subprocess.run")
    @patch("mvmctl.utils._system.require_mvm_group_membership")
    @patch("mvmctl.utils._system.os.getuid", return_value=1000)
    def test_as_non_root_requires_group(
        self, mock_getuid, mock_require, mock_run
    ):
        """When group membership check fails, run_cmd raises PrivilegeError."""
        from mvmctl.exceptions import PrivilegeError

        mock_require.side_effect = PrivilegeError("Not in mvm group")
        with pytest.raises(PrivilegeError):
            run_cmd(["ip", "link"], privileged=True)
        mock_run.assert_not_called()

    @patch("mvmctl.utils._system.subprocess.run")
    @patch("mvmctl.utils._system.os.getuid", return_value=0)
    def test_privileged_false_does_not_add_sudo(self, mock_getuid, mock_run):
        """privileged=False (default) does not add sudo even when called on same args."""
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        run_cmd(["ip", "link"], privileged=False)
        args = mock_run.call_args[0][0]
        assert args == ["ip", "link"]


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

    @patch("mvmctl.utils._system._MVM_GROUP_VERIFIED", False)
    @patch("grp.getgrnam", side_effect=KeyError("mvm"))
    @patch("pwd.getpwnam")
    @patch("mvmctl.utils._system.logger")
    def test_missing_group_warns(self, mock_logger, mock_pwd_unused, mock_grp_unused):
        require_mvm_group_membership()
        mock_logger.warning.assert_called_once()
        warning_msg = mock_logger.warning.call_args[0][0]
        assert "Group" in warning_msg
        assert "does not exist" in warning_msg


# ==============================================================================
# ProcessSignalHandler — _decode_exit_status
# ==============================================================================


class TestProcessSignalHandlerDecode:
    """Tests for ProcessSignalHandler._decode_exit_status."""

    def test_wifexited_exit_0(self):
        from mvmctl.utils._system import ProcessSignalHandler

        assert ProcessSignalHandler._decode_exit_status(0) == 0

    def test_wifexited_exit_1(self):
        from mvmctl.utils._system import ProcessSignalHandler

        assert ProcessSignalHandler._decode_exit_status(256) == 1

    def test_wifsignaled(self):
        from mvmctl.utils._system import ProcessSignalHandler

        assert ProcessSignalHandler._decode_exit_status(9) == 137


# ==============================================================================
# ProcessSignalHandler — _get_process_start_time
# ==============================================================================


class TestProcessSignalHandlerGetStartTime:
    """Tests for ProcessSignalHandler._get_process_start_time."""

    def test_success(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "builtins.open",
            mocker.mock_open(
                read_data="1234 (proc) R 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 99999 0\n"
            ),
        )
        result = ProcessSignalHandler._get_process_start_time(1234)
        assert result == 99999

    def test_file_not_found(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("builtins.open", side_effect=FileNotFoundError)
        assert ProcessSignalHandler._get_process_start_time(9999) is None


# ==============================================================================
# ProcessSignalHandler — _is_pid_reused
# ==============================================================================


class TestProcessSignalHandlerIsPidReused:
    """Tests for ProcessSignalHandler._is_pid_reused."""

    def test_true_when_start_time_differs(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_get_process_start_time", return_value=99999
        )
        assert (
            ProcessSignalHandler._is_pid_reused(
                pid=1234, expected_start_time=12345
            )
            is True
        )

    def test_false_when_same_start_time(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_get_process_start_time", return_value=12345
        )
        assert (
            ProcessSignalHandler._is_pid_reused(
                pid=1234, expected_start_time=12345
            )
            is False
        )

    def test_false_when_process_not_exists(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_get_process_start_time", return_value=None
        )
        assert (
            ProcessSignalHandler._is_pid_reused(
                pid=9999, expected_start_time=12345
            )
            is False
        )


# ==============================================================================
# ProcessSignalHandler — is_alive
# ==============================================================================


class TestProcessSignalHandlerIsAlive:
    """Tests for ProcessSignalHandler.is_alive."""

    def test_reaped_returns_false(self):
        from mvmctl.utils._system import ProcessSignalHandler

        handler = ProcessSignalHandler(pid=1234)
        handler._reaped = True
        assert handler.is_alive() is False

    def test_pid_reused_returns_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_is_pid_reused", return_value=True
        )
        handler = ProcessSignalHandler(pid=1234, expected_start_time=12345)
        assert handler.is_alive() is False

    def test_zombie_returns_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=True
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.is_alive() is False

    def test_zombie_child_reaps(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=True
        )
        mock_reap = mocker.patch.object(ProcessSignalHandler, "_try_reap")
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler.is_alive() is False
        mock_reap.assert_called_once()

    def test_esrch_returns_false(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mock_kill.side_effect = OSError(errno.ESRCH, "No such process")
        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=False
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.is_alive() is False

    def test_eperm_returns_true(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mock_kill.side_effect = OSError(errno.EPERM, "No permission")
        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=False
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.is_alive() is True

    def test_alive_running(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.kill")
        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=False
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.is_alive() is True


# ==============================================================================
# ProcessSignalHandler — send_signal / kill
# ==============================================================================


class TestProcessSignalHandlerSendSignal:
    """Tests for ProcessSignalHandler.send_signal."""

    def test_success_returns_true(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.kill")
        handler = ProcessSignalHandler(pid=1234)
        assert handler.send_signal(signal.SIGTERM) is True

    def test_process_lookup_error_returns_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "mvmctl.utils._system.os.kill", side_effect=ProcessLookupError
        )
        handler = ProcessSignalHandler(pid=9999)
        assert handler.send_signal(signal.SIGTERM) is False

    def test_permission_error_returns_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "mvmctl.utils._system.os.kill", side_effect=PermissionError
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.send_signal(signal.SIGKILL) is False


class TestProcessSignalHandlerKill:
    """Tests for ProcessSignalHandler.kill."""

    def test_calls_send_signal_with_sigkill(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mock_send = mocker.patch.object(
            ProcessSignalHandler, "send_signal", return_value=True
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler.kill() is True
        mock_send.assert_called_once_with(signal.SIGKILL)


# ==============================================================================
# ProcessSignalHandler — _is_zombie
# ==============================================================================


class TestProcessSignalHandlerIsZombie:
    """Tests for ProcessSignalHandler._is_zombie."""

    def test_zombie_state_true(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "builtins.open",
            mocker.mock_open(read_data="1234 (proc) Z 1\n"),
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler._is_zombie() is True

    def test_running_state_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "builtins.open",
            mocker.mock_open(read_data="1234 (proc) R 1\n"),
        )
        handler = ProcessSignalHandler(pid=1234)
        assert handler._is_zombie() is False

    def test_file_not_found_returns_false(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("builtins.open", side_effect=FileNotFoundError)
        handler = ProcessSignalHandler(pid=9999)
        assert handler._is_zombie() is False


# ==============================================================================
# ProcessSignalHandler — _try_reap
# ==============================================================================


class TestProcessSignalHandlerTryReap:
    """Tests for ProcessSignalHandler._try_reap."""

    def test_skips_if_not_child(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mock_waitpid = mocker.patch("mvmctl.utils._system.os.waitpid")
        handler = ProcessSignalHandler(pid=1234, is_child=False)
        handler._try_reap()
        mock_waitpid.assert_not_called()

    def test_skips_if_already_reaped(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mock_waitpid = mocker.patch("mvmctl.utils._system.os.waitpid")
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        handler._reaped = True
        handler._try_reap()
        mock_waitpid.assert_not_called()

    def test_waitpid_success_sets_exit_code(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(1234, 0))
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        handler._try_reap()
        assert handler._reaped is True
        assert handler._exit_code == 0

    def test_waitpid_signaled_sets_128_plus_signal(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(1234, 9))
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        handler._try_reap()
        assert handler._reaped is True
        assert handler._exit_code == 137

    def test_child_process_error_sets_reaped(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "mvmctl.utils._system.os.waitpid", side_effect=ChildProcessError
        )
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        handler._try_reap()
        assert handler._reaped is True


# ==============================================================================
# ProcessSignalHandler — wait_and_capture_exit
# ==============================================================================


class TestProcessSignalHandlerWaitAndCaptureExit:
    """Tests for ProcessSignalHandler.wait_and_capture_exit."""

    def test_already_reaped_returns_cached(self):
        from mvmctl.utils._system import ProcessSignalHandler

        handler = ProcessSignalHandler(pid=1234, is_child=True)
        handler._reaped = True
        handler._exit_code = 42
        assert handler.wait_and_capture_exit() == 42

    def test_not_child_returns_none(self):
        from mvmctl.utils._system import ProcessSignalHandler

        handler = ProcessSignalHandler(pid=1234, is_child=False)
        assert handler.wait_and_capture_exit() is None

    def test_waitpid_success_returns_exit_code(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(1234, 0))
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler.wait_and_capture_exit() == 0
        assert handler._reaped is True

    def test_waitpid_zero_pid_returns_none(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(0, 0))
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler.wait_and_capture_exit() is None

    def test_child_process_error_returns_none_and_reaps(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "mvmctl.utils._system.os.waitpid", side_effect=ChildProcessError
        )
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler.wait_and_capture_exit() is None
        assert handler._reaped is True


# ==============================================================================
# ProcessSignalHandler — graceful_shutdown
# ==============================================================================


class TestProcessSignalHandlerGracefulShutdown:
    """Tests for ProcessSignalHandler.graceful_shutdown."""

    def test_not_alive_returns_exit_code(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "is_alive", return_value=False
        )
        handler = ProcessSignalHandler(pid=1234)
        handler._exit_code = 5
        assert handler.graceful_shutdown() == 5

    def test_pre_signal_hook_false_waits(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mock_wait = mocker.patch.object(
            ProcessSignalHandler, "_wait_for_exit", return_value=3
        )
        handler = ProcessSignalHandler(pid=1234)
        result = handler.graceful_shutdown(pre_signal_hook=lambda: False)
        assert result == 3
        mock_wait.assert_called_once_with(30.0)

    def test_sigterm_then_sigkill_full_flow(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mocker.patch.object(
            ProcessSignalHandler, "_wait_for_exit", side_effect=[None, 9]
        )
        handler = ProcessSignalHandler(pid=1234)
        result = handler.graceful_shutdown()
        assert result == 9
        assert mock_kill.call_count == 2

    def test_sigterm_oserror_esrch_try_reap(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mocker.patch(
            "mvmctl.utils._system.os.kill",
            side_effect=OSError(errno.ESRCH, "No such process"),
        )
        mock_reap = mocker.patch.object(ProcessSignalHandler, "_try_reap")
        handler = ProcessSignalHandler(pid=1234)
        handler._exit_code = 7
        result = handler.graceful_shutdown()
        assert result == 7
        mock_reap.assert_called_once()

    def test_sigterm_oserror_eperm_try_reap(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mocker.patch(
            "mvmctl.utils._system.os.kill",
            side_effect=OSError(errno.EPERM, "No permission"),
        )
        mock_reap = mocker.patch.object(ProcessSignalHandler, "_try_reap")
        handler = ProcessSignalHandler(pid=1234)
        handler._exit_code = 7
        result = handler.graceful_shutdown()
        assert result == 7
        mock_reap.assert_called_once()

    def test_sigkill_oserror_esrch_try_reap(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mock_kill.side_effect = [
            None,
            OSError(errno.ESRCH, "No such process"),
        ]
        mocker.patch.object(
            ProcessSignalHandler, "_wait_for_exit", return_value=None
        )
        mock_reap = mocker.patch.object(ProcessSignalHandler, "_try_reap")
        handler = ProcessSignalHandler(pid=1234)
        handler._exit_code = 3
        result = handler.graceful_shutdown()
        assert result == 3
        mock_reap.assert_called_once()

    def test_sigkill_oserror_eperm_try_reap(self, mocker):
        import errno

        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mock_kill.side_effect = [
            None,
            OSError(errno.EPERM, "No permission"),
        ]
        mocker.patch.object(
            ProcessSignalHandler, "_wait_for_exit", return_value=None
        )
        mock_reap = mocker.patch.object(ProcessSignalHandler, "_try_reap")
        handler = ProcessSignalHandler(pid=1234)
        handler._exit_code = 3
        result = handler.graceful_shutdown()
        assert result == 3
        mock_reap.assert_called_once()

    def test_pre_signal_hook_true_sends_sigterm(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mocker.patch.object(
            ProcessSignalHandler, "_wait_for_exit", return_value=0
        )
        handler = ProcessSignalHandler(pid=1234)
        result = handler.graceful_shutdown(pre_signal_hook=lambda: True)
        assert result == 0
        mock_kill.assert_called_once_with(1234, signal.SIGTERM)


# ==============================================================================
# ProcessSignalHandler — terminate_batch
# ==============================================================================


class TestProcessSignalHandlerTerminateBatch:
    """Tests for ProcessSignalHandler.terminate_batch."""

    def test_empty_pids_returns_empty(self):
        from mvmctl.utils._system import ProcessSignalHandler

        assert ProcessSignalHandler.terminate_batch([]) == []

    def test_sigterm_all_survivors_killed(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mocker.patch("mvmctl.utils._system.time.sleep")
        mock_kill.side_effect = [None] * 6
        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=False
        )
        result = ProcessSignalHandler.terminate_batch([100, 200])
        assert result == [100, 200]
        assert mock_kill.call_count == 6

    def test_sigterm_lookup_skips_terminated(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mock_kill = mocker.patch("mvmctl.utils._system.os.kill")
        mocker.patch("mvmctl.utils._system.time.sleep")
        mock_kill.side_effect = [
            None,
            ProcessLookupError(),
            None,
            None,
        ]
        mocker.patch.object(
            ProcessSignalHandler, "_is_zombie", return_value=False
        )
        result = ProcessSignalHandler.terminate_batch([100, 200])
        assert result == [100]


# ==============================================================================
# ProcessSignalHandler — _wait_for_exit
# ==============================================================================


class TestProcessSignalHandlerWaitForExit:
    """Tests for ProcessSignalHandler._wait_for_exit."""

    def test_child_waitpid_success(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(1234, 0))
        mocker.patch("mvmctl.utils._system.time.monotonic", return_value=100.0)
        mocker.patch("mvmctl.utils._system.time.sleep")
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler._wait_for_exit(5.0) == 0
        assert handler._reaped is True

    def test_child_waitpid_none_found_then_timeout(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch("mvmctl.utils._system.os.waitpid", return_value=(0, 0))
        mocker.patch(
            "mvmctl.utils._system.time.monotonic", side_effect=[100.0, 106.0]
        )
        mocker.patch("mvmctl.utils._system.time.sleep")
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler._wait_for_exit(5.0) is None

    def test_child_child_process_error(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch(
            "mvmctl.utils._system.os.waitpid", side_effect=ChildProcessError
        )
        mocker.patch("mvmctl.utils._system.time.monotonic", return_value=100.0)
        mocker.patch("mvmctl.utils._system.time.sleep")
        handler = ProcessSignalHandler(pid=1234, is_child=True)
        assert handler._wait_for_exit(5.0) is None
        assert handler._reaped is True

    def test_not_child_alive_then_timeout(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(ProcessSignalHandler, "is_alive", return_value=True)
        mocker.patch(
            "mvmctl.utils._system.time.monotonic", side_effect=[100.0, 106.0]
        )
        mocker.patch("mvmctl.utils._system.time.sleep")
        handler = ProcessSignalHandler(pid=1234, is_child=False)
        assert handler._wait_for_exit(5.0) is None

    def test_not_child_dead_returns_exit_code(self, mocker):
        from mvmctl.utils._system import ProcessSignalHandler

        mocker.patch.object(
            ProcessSignalHandler, "is_alive", return_value=False
        )
        mocker.patch(
            "mvmctl.utils._system.time.monotonic", side_effect=[100.0, 100.5]
        )
        mocker.patch("mvmctl.utils._system.time.sleep")
        handler = ProcessSignalHandler(pid=1234, is_child=False)
        assert handler._wait_for_exit(5.0) is None
        assert handler._reaped is True


# ==============================================================================
# has_python_ancestor — additional paths
# ==============================================================================


class TestHasPythonAncestorNew:
    """Additional tests for has_python_ancestor()."""

    def test_finds_mvm_in_cmdline(self, mocker):
        from mvmctl.utils._system import has_python_ancestor

        mocker.patch(
            "builtins.open",
            mocker.mock_open(read_data=b"mvmctl\x00start\x00myvm"),
        )
        assert has_python_ancestor(1234) is True

    def test_finds_python_in_cmdline(self, mocker):
        from mvmctl.utils._system import has_python_ancestor

        mocker.patch(
            "builtins.open",
            mocker.mock_open(read_data=b"/usr/bin/python3\x00script.py"),
        )
        assert has_python_ancestor(1234) is True

    def test_cmdline_not_found_returns_false(self, mocker):
        from mvmctl.utils._system import has_python_ancestor

        mocker.patch("builtins.open", side_effect=FileNotFoundError)
        assert has_python_ancestor(1234) is False

    def test_cmdline_permission_error_returns_false(self, mocker):
        from mvmctl.utils._system import has_python_ancestor

        mocker.patch("builtins.open", side_effect=PermissionError)
        assert has_python_ancestor(1234) is False

    def test_ppid_not_found_breaks(self, mocker):
        """Status file exists but no PPid line -> break -> return False."""
        from io import BytesIO, StringIO

        from mvmctl.utils._system import has_python_ancestor

        registry: dict[str, BytesIO | StringIO] = {}

        def _open(path, *args, **kwargs):
            if path not in registry:
                if path == "/proc/1234/cmdline":
                    registry[path] = BytesIO(b"some_binary")
                elif path == "/proc/1234/status":
                    registry[path] = StringIO(
                        "Name:   someproc\nUid:    1000\n"
                    )
            buf = registry.get(path)
            if buf is not None:
                buf.seek(0)
                return buf
            raise FileNotFoundError(2, "No such file", path)

        mocker.patch("builtins.open", side_effect=_open)
        assert has_python_ancestor(1234) is False

    def test_ppid_one_exits_loop(self, mocker):
        """PPid=1 -> loop exits -> returns False."""
        from io import BytesIO, StringIO

        from mvmctl.utils._system import has_python_ancestor

        registry: dict[str, BytesIO | StringIO] = {}

        def _open(path, *args, **kwargs):
            if path not in registry:
                if path == "/proc/1234/cmdline":
                    registry[path] = BytesIO(b"some_binary")
                elif path == "/proc/1234/status":
                    registry[path] = StringIO("Name:   someproc\nPPid:   1\n")
            buf = registry.get(path)
            if buf is not None:
                buf.seek(0)
                return buf
            raise FileNotFoundError(2, "No such file", path)

        mocker.patch("builtins.open", side_effect=_open)
        assert has_python_ancestor(1234) is False


# ==============================================================================
# stream_cmd — additional edge cases
# ==============================================================================


class TestStreamCmdNew:
    """Additional tests for stream_cmd()."""

    def test_stdout_none_raises(self, mocker):
        from mvmctl.utils._system import stream_cmd

        mock_proc = MagicMock()
        mock_proc.stdout = None
        mocker.patch(
            "mvmctl.utils._system.subprocess.Popen", return_value=mock_proc
        )
        with pytest.raises(ProcessError, match="stdout is None"):
            list(stream_cmd(["mycmd"]))


# ==============================================================================
# require_mvm_group_membership — additional paths
# ==============================================================================


@pytest.mark.real_mvm_group_check
@patch("mvmctl.utils._system._MVM_GROUP_VERIFIED", False)
class TestRequireMvmGroupMembershipNew:
    """Additional tests for require_mvm_group_membership()."""

    @patch("grp.getgrnam")
    @patch("pwd.getpwuid")
    @patch("mvmctl.utils._system.os.getgroups", return_value=[1001, 1002])
    @patch("mvmctl.utils._system.os.getgid", return_value=1000)
    @patch("mvmctl.utils._system.os.getegid", return_value=1000)
    def test_supplementary_member_succeeds(
        self, mock_egid, mock_gid, mock_groups, mock_pwd, mock_grp
    ):
        from mvmctl.utils._system import require_mvm_group_membership

        mock_grp.return_value = MagicMock(gr_gid=1001, gr_mem=["testuser"])
        mock_pwd.return_value = MagicMock(pw_name="testuser", pw_gid=1000)
        require_mvm_group_membership()

    @patch("grp.getgrnam")
    @patch("pwd.getpwuid")
    @patch("mvmctl.utils._system.os.getgroups", return_value=[1000, 1001])
    @patch("mvmctl.utils._system.os.getgid", return_value=1000)
    @patch("mvmctl.utils._system.os.getegid", return_value=1000)
    def test_primary_group_member_succeeds(
        self, mock_egid, mock_gid, mock_groups, mock_pwd, mock_grp
    ):
        from mvmctl.utils._system import require_mvm_group_membership

        mock_grp.return_value = MagicMock(gr_gid=1001, gr_mem=["otheruser"])
        mock_pwd.return_value = MagicMock(pw_name="testuser", pw_gid=1001)
        require_mvm_group_membership()

    @patch("grp.getgrnam")
    @patch("pwd.getpwuid")
    @patch("mvmctl.utils._system.logger")
    def test_user_not_in_group_warns(self, mock_logger, mock_pwd, mock_grp):
        from mvmctl.utils._system import require_mvm_group_membership

        mock_grp.return_value = MagicMock(gr_gid=1001, gr_mem=["otheruser"])
        mock_pwd.return_value = MagicMock(pw_name="testuser", pw_gid=1000)
        require_mvm_group_membership()
        mock_logger.warning.assert_called_once()
        warning_msg = mock_logger.warning.call_args[0][0]
        assert "is not in the" in warning_msg

    @patch("grp.getgrnam")
    @patch("pwd.getpwuid")
    @patch("mvmctl.utils._system.os.getgroups", return_value=[1000, 1002])
    @patch("mvmctl.utils._system.os.getgid", return_value=1000)
    @patch("mvmctl.utils._system.os.getegid", return_value=1000)
    @patch("mvmctl.utils._system.logger")
    def test_process_gid_not_active_warns(
        self, mock_logger, mock_egid, mock_gid, mock_groups, mock_pwd, mock_grp
    ):
        from mvmctl.utils._system import require_mvm_group_membership

        mock_grp.return_value = MagicMock(gr_gid=1001, gr_mem=["testuser"])
        mock_pwd.return_value = MagicMock(pw_name="testuser", pw_gid=1000)
        require_mvm_group_membership()
        mock_logger.warning.assert_called_once()
        warning_msg = mock_logger.warning.call_args[0][0]
        assert "does not have the group active" in warning_msg
