from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core.logs import get_log_path, read_log_lines, show_logs
from mvmctl.exceptions import ConfigError, MVMError, VMNotFoundError


def test_get_log_path_boot(tmp_path: Path) -> None:
    vm_dir = tmp_path / "test-vm"
    vm_dir.mkdir()
    log_file = vm_dir / "firecracker.console.log"
    log_file.write_text("boot output\n")

    with patch("mvmctl.core.logs.get_vm_dir_by_hash", return_value=vm_dir):
        result = get_log_path("a" * 64, log_type="boot")

    assert result == log_file


def test_get_log_path_os(tmp_path: Path) -> None:
    vm_dir = tmp_path / "test-vm"
    vm_dir.mkdir()
    log_file = vm_dir / "firecracker.log"
    log_file.write_text("os log\n")

    with patch("mvmctl.core.logs.get_vm_dir_by_hash", return_value=vm_dir):
        result = get_log_path("a" * 64, log_type="os")

    assert result == log_file


def test_get_log_path_unknown_type(tmp_path: Path) -> None:
    vm_dir = tmp_path / "test-vm"
    vm_dir.mkdir()

    with patch("mvmctl.core.logs.get_vm_dir_by_hash", return_value=vm_dir):
        with pytest.raises(ConfigError, match="Unknown log type"):
            get_log_path("a" * 64, log_type="unknown")


def test_get_log_path_missing_vm(tmp_path: Path) -> None:
    nonexistent = tmp_path / "no-such-vm"

    with patch("mvmctl.core.logs.get_vm_dir_by_hash", return_value=nonexistent):
        with pytest.raises(VMNotFoundError, match="not found"):
            get_log_path("b" * 64)


def test_get_log_path_missing_file(tmp_path: Path) -> None:
    vm_dir = tmp_path / "test-vm"
    vm_dir.mkdir()

    with patch("mvmctl.core.logs.get_vm_dir_by_hash", return_value=vm_dir):
        with pytest.raises(VMNotFoundError, match="Log file not found"):
            get_log_path("a" * 64, log_type="boot")


def test_read_log_lines_basic(tmp_path: Path) -> None:
    log_file = tmp_path / "test.log"
    log_file.write_text("".join(f"line {i}\n" for i in range(100)))

    result = read_log_lines(log_file, lines=10)

    assert len(result) == 10
    assert result[0] == "line 90\n"
    assert result[-1] == "line 99\n"


def test_read_log_lines_fewer_than_requested(tmp_path: Path) -> None:
    log_file = tmp_path / "test.log"
    log_file.write_text("line 0\nline 1\nline 2\n")

    result = read_log_lines(log_file, lines=50)

    assert len(result) == 3
    assert result[0] == "line 0\n"
    assert result[-1] == "line 2\n"


def test_show_logs_success(tmp_path: Path) -> None:
    log_file = tmp_path / "firecracker.console.log"
    log_file.write_text("boot line 1\nboot line 2\n")

    with patch("mvmctl.core.logs.get_log_path", return_value=log_file):
        result = show_logs("test-vm", log_type="boot", lines=50)

    assert isinstance(result, list)
    assert len(result) == 2
    assert result[0] == "boot line 1\n"


def test_show_logs_not_found() -> None:
    with patch(
        "mvmctl.core.logs.get_log_path",
        side_effect=VMNotFoundError("VM 'nonexistent-vm' not found"),
    ):
        with pytest.raises(VMNotFoundError, match="not found"):
            show_logs("nonexistent-vm", log_type="boot")


def test_read_log_lines_io_error(tmp_path: Path) -> None:
    log_file = tmp_path / "missing.log"
    with pytest.raises(MVMError, match="Error reading log file"):
        read_log_lines(log_file, lines=10)


def test_show_logs_os_type(tmp_path: Path) -> None:
    log_file = tmp_path / "firecracker.log"
    log_file.write_text("os line 1\nos line 2\n")

    with patch("mvmctl.core.logs.get_log_path", return_value=log_file):
        result = show_logs("test-vm", log_type="os", lines=50)

    assert isinstance(result, list)
    assert len(result) == 2


def test_show_logs_follow(tmp_path: Path) -> None:
    from mvmctl.core.logs import follow_log

    log_file = tmp_path / "test.log"
    log_file.write_text("line 1\nline 2\n")

    gen = follow_log(log_file)
    log_file.write_text("line 1\nline 2\nline 3\n")

    with patch("time.sleep", side_effect=KeyboardInterrupt):
        lines = []
        try:
            for line in gen:
                lines.append(line)
        except KeyboardInterrupt:
            pass


def test_show_logs_follow_keyboard_interrupt(tmp_path: Path) -> None:
    log_file = tmp_path / "test.log"
    log_file.write_text("line 1\n")

    def fake_follow(f):
        yield "line 1"
        raise KeyboardInterrupt

    with (
        patch("mvmctl.core.logs.get_log_path", return_value=log_file),
        patch("mvmctl.core.logs.follow_log", side_effect=fake_follow),
    ):
        result = show_logs("test-vm", log_type="boot", follow=True)

    assert isinstance(result, list)
    assert "line 1" in result


def test_follow_log_io_error(tmp_path: Path) -> None:
    from mvmctl.core.logs import follow_log

    log_file = tmp_path / "nonexistent.log"
    gen = follow_log(log_file)
    with pytest.raises(MVMError, match="Error following log"):
        list(gen)
