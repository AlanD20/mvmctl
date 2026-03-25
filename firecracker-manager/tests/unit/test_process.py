"""Tests for utils/process.py."""

import subprocess
from unittest.mock import MagicMock, patch

import pytest

from fcm.exceptions import ProcessError
from fcm.utils.process import run_cmd, stream_cmd


# ---------------------------------------------------------------------------
# run_cmd
# ---------------------------------------------------------------------------


def test_run_cmd_success():
    result = run_cmd(["echo", "hello"])
    assert result.returncode == 0
    assert result.stdout.strip() == "hello"


def test_run_cmd_returns_completed_process():
    result = run_cmd(["true"])
    assert isinstance(result, subprocess.CompletedProcess)
    assert result.returncode == 0


def test_run_cmd_command_not_found():
    with pytest.raises(ProcessError, match="Command not found"):
        run_cmd(["nonexistent_binary_xyz_12345"])


def test_run_cmd_command_fails():
    with pytest.raises(ProcessError, match="exit 1"):
        run_cmd(["false"])


def test_run_cmd_failure_includes_stderr():
    # Error message should only show command name, not full path
    with pytest.raises(ProcessError, match="(?i)no such file"):
        run_cmd(["ls", "/nonexistent_path_xyz_12345"])


def test_run_cmd_with_cwd(tmp_path):
    result = run_cmd(["pwd"], cwd=str(tmp_path))
    assert result.stdout.strip() == str(tmp_path.resolve())


def test_run_cmd_check_false_returns_on_failure():
    result = run_cmd(["false"], check=False)
    assert result.returncode != 0


def test_run_cmd_check_false_no_exception():
    result = run_cmd(["ls", "/nonexistent_path_xyz_12345"], check=False)
    assert result.returncode != 0


def test_run_cmd_captures_stdout():
    result = run_cmd(["printf", "line1\nline2\n"])
    assert "line1" in result.stdout
    assert "line2" in result.stdout


def test_run_cmd_captures_stderr_on_failure():
    with pytest.raises(ProcessError) as exc_info:
        run_cmd(["ls", "/nonexistent_path_xyz_12345"])
    error_str = str(exc_info.value)
    # Error message should only show command name (ls), not full arguments
    assert "ls" in error_str
    assert "exit" in error_str
    # Command arguments should NOT be exposed in error message
    assert "/nonexistent_path_xyz_12345" not in error_str.split("\n")[0]


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_file_not_found_mocked(mock_run):
    mock_run.side_effect = FileNotFoundError("no such binary")
    with pytest.raises(ProcessError, match="Command not found: badcmd"):
        run_cmd(["badcmd", "--arg"])


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_called_process_error_mocked(mock_run):
    err = subprocess.CalledProcessError(2, "mycmd")
    err.stderr = "some error detail"
    mock_run.side_effect = err
    # Error message should only show command name, not full arguments
    with pytest.raises(ProcessError, match="exit 2.*mycmd"):
        run_cmd(["mycmd"])


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_called_process_error_no_stderr(mock_run):
    err = subprocess.CalledProcessError(1, "mycmd")
    err.stderr = ""
    mock_run.side_effect = err
    with pytest.raises(ProcessError, match="exit 1"):
        run_cmd(["mycmd"])


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_called_process_error_none_stderr(mock_run):
    err = subprocess.CalledProcessError(1, "mycmd")
    err.stderr = None
    mock_run.side_effect = err
    with pytest.raises(ProcessError, match="exit 1"):
        run_cmd(["mycmd"])


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_sanitized_error_message(mock_run):
    """Test that error messages only show command name, not full arguments."""
    err = subprocess.CalledProcessError(1, "mycmd")
    err.stderr = "sensitive error details"
    mock_run.side_effect = err
    with pytest.raises(ProcessError) as exc_info:
        run_cmd(["mycmd", "--secret-arg", "password123"])
    error_str = str(exc_info.value)
    # Should show command name
    assert "mycmd" in error_str
    # Should NOT show full arguments
    assert "--secret-arg" not in error_str
    assert "password123" not in error_str


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_sanitized_stderr_truncated(mock_run):
    """Test that stderr is limited to 100 characters in error messages."""
    err = subprocess.CalledProcessError(1, "mycmd")
    err.stderr = "x" * 200
    mock_run.side_effect = err
    with pytest.raises(ProcessError) as exc_info:
        run_cmd(["mycmd"])
    error_str = str(exc_info.value)
    # Should be truncated with ...
    assert "..." in error_str
    # Should not contain the full 200 characters
    assert len(error_str) < 250


@patch("fcm.utils.process.logger")
@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_logs_full_failure_details(mock_run, mock_logger):
    full_stderr = "sensitive stderr details " + "x" * 200
    err = subprocess.CalledProcessError(1, ["mycmd", "--secret-arg", "password123"])
    err.stderr = full_stderr
    mock_run.side_effect = err

    with pytest.raises(ProcessError):
        run_cmd(["mycmd", "--secret-arg", "password123"])

    failure_logs = [
        call
        for call in mock_logger.debug.call_args_list
        if call.args[0].startswith("Command failed")
    ]
    assert len(failure_logs) == 1
    failure_log = failure_logs[0]
    assert failure_log.args[1] == 1
    assert failure_log.args[2] == "mycmd --secret-arg password123"
    assert failure_log.args[3] == full_stderr
    assert failure_log.kwargs["exc_info"] is True


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_passes_cwd(mock_run):
    mock_run.return_value = MagicMock(returncode=0)
    run_cmd(["ls"], cwd="/tmp")
    mock_run.assert_called_once_with(
        ["ls"],
        capture_output=True,
        text=True,
        check=True,
        cwd="/tmp",
    )


@patch("fcm.utils.process.subprocess.run")
def test_run_cmd_capture_false(mock_run):
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


def test_stream_cmd_yields_lines():
    lines = list(stream_cmd(["printf", "line1\nline2\nline3\n"]))
    assert lines == ["line1", "line2", "line3"]


def test_stream_cmd_strips_trailing_newlines():
    lines = list(stream_cmd(["echo", "hello"]))
    assert lines == ["hello"]
    assert not any(line.endswith("\n") for line in lines)


def test_stream_cmd_command_not_found():
    with pytest.raises(ProcessError, match="Command not found"):
        list(stream_cmd(["nonexistent_binary_xyz_12345"]))


def test_stream_cmd_failure_raises_after_output():
    with pytest.raises(ProcessError, match="exit"):
        list(stream_cmd(["bash", "-c", "echo output && exit 42"]))


def test_stream_cmd_empty_output():
    lines = list(stream_cmd(["true"]))
    assert lines == []


def test_stream_cmd_with_cwd(tmp_path):
    lines = list(stream_cmd(["pwd"], cwd=str(tmp_path)))
    assert lines == [str(tmp_path.resolve())]


def test_stream_cmd_stderr_merged_to_stdout():
    lines = list(stream_cmd(["bash", "-c", "echo out && echo err >&2"]))
    assert "out" in lines
    assert "err" in lines


@patch("fcm.utils.process.subprocess.Popen")
def test_stream_cmd_file_not_found_mocked(mock_popen):
    mock_popen.side_effect = FileNotFoundError("no such binary")
    with pytest.raises(ProcessError, match="Command not found: badcmd"):
        list(stream_cmd(["badcmd"]))


def _make_mock_stdout(lines):
    mock_stdout = MagicMock()
    mock_stdout.__iter__ = MagicMock(return_value=iter(lines))
    return mock_stdout


@patch("fcm.utils.process.subprocess.Popen")
def test_stream_cmd_non_zero_exit_mocked(mock_popen):
    mock_proc = MagicMock()
    mock_proc.stdout = _make_mock_stdout(["line1\n", "line2\n"])
    mock_proc.wait.return_value = 5
    mock_popen.return_value = mock_proc
    with pytest.raises(ProcessError, match="exit 5"):
        list(stream_cmd(["failcmd"]))


@patch("fcm.utils.process.subprocess.Popen")
def test_stream_cmd_success_mocked(mock_popen):
    mock_proc = MagicMock()
    mock_proc.stdout = _make_mock_stdout(["hello\n", "world\n"])
    mock_proc.wait.return_value = 0
    mock_popen.return_value = mock_proc
    lines = list(stream_cmd(["mycmd"]))
    assert lines == ["hello", "world"]


@patch("fcm.utils.process.subprocess.Popen")
def test_stream_cmd_passes_cwd_mocked(mock_popen):
    mock_proc = MagicMock()
    mock_proc.stdout = _make_mock_stdout([])
    mock_proc.wait.return_value = 0
    mock_popen.return_value = mock_proc
    list(stream_cmd(["ls"], cwd="/tmp"))
    mock_popen.assert_called_once_with(
        ["ls"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        cwd="/tmp",
    )
