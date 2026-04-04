"""Tests for VM manager."""

from pathlib import Path

import pytest

from mvmctl.core.vm_manager import VMManager
from mvmctl.models.vm import VMInstance, VMStatus


@pytest.mark.parametrize(
    "vm_name,pid,ipv4",
    [
        ("test-vm", 1234, "10.0.0.2"),
        ("my-vm", 5678, "10.0.0.5"),
        ("vm123", 9999, "192.168.1.10"),
    ],
)
def test_vm_manager_register(vm_manager: VMManager, vm_name: str, pid: int, ipv4: str):
    """register should store a VMInstance that is retrievable by name with correct attributes."""
    vm = VMInstance(
        name=vm_name,
        pid=pid,
        ipv4=ipv4,
        status=VMStatus.RUNNING,
    )

    vm_manager.register(vm)

    retrieved = vm_manager.get(vm_name)
    assert retrieved is not None
    assert retrieved.name == vm_name
    assert retrieved.pid == pid
    assert retrieved.ipv4 == ipv4
    assert retrieved.status == VMStatus.RUNNING


def test_vm_manager_list(vm_manager: VMManager):
    """list_all should return all registered VMs."""
    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMStatus.RUNNING))
    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMStatus.STOPPED))

    vms = vm_manager.list_all()
    assert len(vms) == 2


def test_vm_manager_count_vms(vm_manager: VMManager):
    """count_vms should return the number of VMs without loading full metadata."""
    assert vm_manager.count_vms() == 0

    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMStatus.RUNNING))
    assert vm_manager.count_vms() == 1

    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMStatus.STOPPED))
    assert vm_manager.count_vms() == 2

    registered = vm_manager.get("vm1")
    assert registered is not None
    vm_manager.deregister(registered.id)
    assert vm_manager.count_vms() == 1


def test_vm_manager_deregister(vm_manager: VMManager):
    vm = VMInstance(name="test-vm", pid=1234, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("test-vm")
    assert registered is not None

    vm_manager.deregister(registered.id)
    assert vm_manager.get("test-vm") is None


@pytest.mark.parametrize("vm_name", ["non-existent", "ghost-vm", "missing-123"])
def test_vm_manager_not_found(vm_manager: VMManager, vm_name: str):
    """get should return None when a VM with the given name has not been registered."""
    result = vm_manager.get(vm_name)
    assert result is None


@pytest.mark.parametrize(
    "vm_name,new_status",
    [
        ("nonexistent", VMStatus.STOPPED),
        ("ghost-vm", VMStatus.RUNNING),
        ("missing-vm", VMStatus.STOPPED),
    ],
)
def test_vm_manager_update_status_not_found(
    vm_manager: VMManager, vm_name: str, new_status: VMStatus
):
    """update_status should raise VMNotFoundError when the named VM does not exist."""
    from mvmctl.exceptions import VMNotFoundError

    with pytest.raises(VMNotFoundError):
        vm_manager.update_status(vm_name, new_status)


def test_vm_manager_find_by_id_prefix(vm_manager: VMManager):
    vm = VMInstance(name="myvm", pid=1, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("myvm")
    assert registered is not None
    prefix = registered.id[:6]
    matches = vm_manager.find_by_id_prefix(prefix)
    assert len(matches) == 1
    assert matches[0].name == "myvm"


def test_vm_manager_find_by_id_prefix_no_match(vm_manager: VMManager):
    assert vm_manager.find_by_id_prefix("zzzzzz") == []


def test_vm_manager_get_by_id_prefix_unique(vm_manager: VMManager):
    vm = VMInstance(name="uniquevm", pid=2, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("uniquevm")
    assert registered is not None
    result = vm_manager.get_by_id_prefix(registered.id[:6])
    assert result is not None
    assert result.name == "uniquevm"


def test_vm_manager_get_by_full_id_exact_match(vm_manager: VMManager):
    """Test that get_by_full_id returns exact match by full 16-char hash."""
    vm = VMInstance(name="testvm", pid=1, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("testvm")
    assert registered is not None

    # Get by full ID should work
    result = vm_manager.get_by_full_id(registered.id)
    assert result is not None
    assert result.name == "testvm"
    assert result.id == registered.id


def test_vm_manager_get_by_full_id_no_match(vm_manager: VMManager):
    """Test that get_by_full_id returns None for non-existent hash."""
    result = vm_manager.get_by_full_id("a" * 16)
    assert result is None


def test_vm_manager_get_by_full_id_collision_resistance(vm_manager: VMManager):
    """Test that get_by_full_id handles VMs with same prefix correctly."""
    vm1 = VMInstance(name="vm1", pid=1, status=VMStatus.RUNNING, id="abc123" + "a" * 10)
    vm2 = VMInstance(name="vm2", pid=2, status=VMStatus.RUNNING, id="abc123" + "b" * 10)

    vm_manager.register(vm1)
    vm_manager.register(vm2)

    # get_by_id_prefix should return None (ambiguous)
    prefix_result = vm_manager.get_by_id_prefix("abc123")
    assert prefix_result is None

    # get_by_full_id should return correct VM for each full ID
    result1 = vm_manager.get_by_full_id(vm1.id)
    assert result1 is not None
    assert result1.name == "vm1"

    result2 = vm_manager.get_by_full_id(vm2.id)
    assert result2 is not None
    assert result2.name == "vm2"


def test_vm_manager_get_by_name_returns_single(vm_manager: VMManager):
    vm = VMInstance(name="dup", pid=1, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    results = vm_manager.get_by_name("dup")
    assert len(results) == 1
    assert results[0].name == "dup"


def test_vm_manager_update_status_success(vm_manager: VMManager):
    vm = VMInstance(name="statusvm", pid=3, status=VMStatus.RUNNING)
    vm_manager.register(vm)
    vm_manager.update_status("statusvm", VMStatus.STOPPED)
    updated = vm_manager.get("statusvm")
    assert updated is not None
    assert updated.status == VMStatus.STOPPED


def test_vm_manager_persists_to_sqlite(tmp_path: Path):
    """VMManager persists VMs to SQLite database."""
    from datetime import datetime, timezone

    mgr = VMManager()
    vm = VMInstance(
        name="testvm",
        pid=42,
        ipv4="10.0.0.2",
        status=VMStatus.RUNNING,
        created_at=datetime.now(tz=timezone.utc),
    )
    mgr.register(vm)

    vms = mgr.list_all()
    assert len(vms) == 1
    assert vms[0].name == "testvm"
    assert vms[0].pid == 42
    assert len(vms[0].id) == 16


# ---------------------------------------------------------------------------
# Exit code tracking tests (Phase 4)
# ---------------------------------------------------------------------------


def test_get_vm_status_with_exit_code_running(mocker, sample_vm):
    """Verify 'running' status when process alive."""

    sample_vm.pid = 1234
    sample_vm.id = "a" * 64

    # Mock os.kill(1234, 0) succeeds
    mock_kill = mocker.patch("os.kill", return_value=None)

    # Import and call the function
    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns "running"
    assert status == "running"
    mock_kill.assert_called_once_with(1234, 0)


def test_get_vm_status_with_exit_code_from_log(mocker, sample_vm, tmp_path):
    """Verify 'exited(N)' when exit code found in log."""

    sample_vm.pid = 1234
    sample_vm.id = "a" * 64
    sample_vm.name = "testvm"

    # Mock os.kill raises ProcessLookupError (process not running)
    mocker.patch("os.kill", side_effect=ProcessLookupError())

    # Create firecracker.log with "exit code: 1"
    vm_dir = tmp_path / "vms" / sample_vm.id
    vm_dir.mkdir(parents=True)
    log_file = vm_dir / "firecracker.log"
    log_file.write_text("Some log line\nexit code: 1\nAnother line")

    # Mock get_vm_dir to return our tmp path
    mocker.patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir)

    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns "exited(1)"
    assert status == "exited(1)"


def test_get_vm_status_with_exit_code_from_status_file(mocker, sample_vm, tmp_path):
    """Verify 'exited(N)' when exit code in status file."""

    sample_vm.pid = 1234
    sample_vm.id = "a" * 64
    sample_vm.name = "testvm"

    # Mock os.kill raises ProcessLookupError
    mocker.patch("os.kill", side_effect=ProcessLookupError())

    # Create firecracker.exitcode file with "1"
    vm_dir = tmp_path / "vms" / sample_vm.id
    vm_dir.mkdir(parents=True)
    exitcode_file = vm_dir / "firecracker.exitcode"
    exitcode_file.write_text("1")

    # Mock get_vm_dir to return our tmp path
    mocker.patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir)

    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns "exited(1)"
    assert status == "exited(1)"


def test_get_vm_status_exited_no_code(mocker, sample_vm, tmp_path):
    """Verify 'exited' when no exit code available."""

    sample_vm.pid = 1234
    sample_vm.id = "a" * 64
    sample_vm.name = "testvm"

    # Mock os.kill raises ProcessLookupError
    mocker.patch("os.kill", side_effect=ProcessLookupError())

    # Create VM dir but NO log file, NO status file
    vm_dir = tmp_path / "vms" / sample_vm.id
    vm_dir.mkdir(parents=True)

    # Mock get_vm_dir to return our tmp path
    mocker.patch("mvmctl.utils.fs.get_vm_dir_by_hash", return_value=vm_dir)

    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns "exited" (no code)
    assert status == "exited"


def test_get_vm_status_no_pid(mocker, sample_vm):
    """Verify original status when PID is None."""
    sample_vm.pid = None
    sample_vm.status = VMStatus.STOPPED

    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns sample_vm.status
    assert status == VMStatus.STOPPED


def test_get_exit_code_from_log_parses_various_formats(mocker, sample_vm, tmp_path):
    """Verify log parsing handles multiple exit code formats."""
    from mvmctl.core.vm_manager import _get_exit_code_from_log

    test_cases = [
        ("exit code: 1", 1),
        ("exited: 1", 1),
        ("exit 1", 1),
        ("exit code: 255", 255),
        ("exited: 0", 0),
        ("Exit Code: 42", 42),
        ("EXIT CODE: 99", 99),
    ]

    for log_content, expected_code in test_cases:
        log_file = tmp_path / "firecracker.log"
        log_file.write_text(f"Some log\n{log_content}\nMore log")

        result = _get_exit_code_from_log(log_file)

        assert result == expected_code, f"Failed for format: {log_content}"


def test_get_exit_code_from_log_no_match(mocker, sample_vm, tmp_path):
    """Verify None when log exists but no exit code pattern."""
    from mvmctl.core.vm_manager import _get_exit_code_from_log

    # Create firecracker.log without exit code
    log_file = tmp_path / "firecracker.log"
    log_file.write_text("Some log line\nAnother log line\nNo exit code here")

    result = _get_exit_code_from_log(log_file)

    # Verify returns None
    assert result is None


def test_get_exit_code_from_log_file_not_exists(tmp_path):
    """Verify None when log file does not exist."""
    from mvmctl.core.vm_manager import _get_exit_code_from_log

    log_file = tmp_path / "nonexistent.log"
    result = _get_exit_code_from_log(log_file)
    assert result is None


def test_is_hex_string_invalid_chars():
    """_is_hex_string should return False for non-hex characters."""
    from mvmctl.core.vm_manager import _is_hex_string

    assert _is_hex_string("gggggggggggggggg") is False
    assert _is_hex_string("xyz123456789abcd") is False


def test_is_hex_string_wrong_length():
    """_is_hex_string should return False for wrong length."""
    from mvmctl.core.vm_manager import _is_hex_string

    assert _is_hex_string("abc", length=16) is False
    assert _is_hex_string("a" * 32, length=16) is False


def test_is_hex_string_valid():
    """_is_hex_string should return True for valid hex string."""
    from mvmctl.core.vm_manager import _is_hex_string

    assert _is_hex_string("0123456789abcdef") is True
    assert _is_hex_string("a" * 16) is True


def test_get_by_name_returns_empty_list(vm_manager: VMManager):
    """get_by_name should return empty list when no VM matches."""
    results = vm_manager.get_by_name("nonexistent")
    assert results == []
