"""Tests for VM manager."""

from pathlib import Path

import pytest

from mvmctl.core.vm_manager import VMManager
from mvmctl.models.vm import VMInstance, VMState


@pytest.mark.parametrize(
    "vm_name,pid,ip",
    [
        ("test-vm", 1234, "10.0.0.2"),
        ("my-vm", 5678, "10.0.0.5"),
        ("vm123", 9999, "192.168.1.10"),
    ],
)
def test_vm_manager_register(vm_manager: VMManager, vm_name: str, pid: int, ip: str):
    """register should store a VMInstance that is retrievable by name with correct attributes."""
    vm = VMInstance(
        name=vm_name,
        pid=pid,
        ip=ip,
        status=VMState.RUNNING,
    )

    vm_manager.register(vm)

    retrieved = vm_manager.get(vm_name)
    assert retrieved is not None
    assert retrieved.name == vm_name
    assert retrieved.pid == pid
    assert retrieved.ip == ip
    assert retrieved.status == VMState.RUNNING


def test_vm_manager_list(vm_manager: VMManager):
    """list_all should return all registered VMs."""
    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMState.RUNNING))
    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMState.STOPPED))

    vms = vm_manager.list_all()
    assert len(vms) == 2


def test_vm_manager_count_vms(vm_manager: VMManager):
    """count_vms should return the number of VMs without loading full metadata."""
    assert vm_manager.count_vms() == 0

    vm_manager.register(VMInstance(name="vm1", pid=1, status=VMState.RUNNING))
    assert vm_manager.count_vms() == 1

    vm_manager.register(VMInstance(name="vm2", pid=2, status=VMState.STOPPED))
    assert vm_manager.count_vms() == 2

    registered = vm_manager.get("vm1")
    assert registered is not None
    vm_manager.deregister(registered.id)
    assert vm_manager.count_vms() == 1


def test_vm_manager_deregister(vm_manager: VMManager):
    vm = VMInstance(name="test-vm", pid=1234, status=VMState.RUNNING)
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
        ("nonexistent", VMState.STOPPED),
        ("ghost-vm", VMState.RUNNING),
        ("missing-vm", VMState.STOPPED),
    ],
)
def test_vm_manager_update_status_not_found(
    vm_manager: VMManager, vm_name: str, new_status: VMState
):
    """update_status should raise VMNotFoundError when the named VM does not exist."""
    from mvmctl.exceptions import VMNotFoundError

    with pytest.raises(VMNotFoundError):
        vm_manager.update_status(vm_name, new_status)


def test_vm_manager_find_by_short_id(vm_manager: VMManager):
    vm = VMInstance(name="myvm", pid=1, status=VMState.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("myvm")
    assert registered is not None
    short_id = registered.id[:6]
    matches = vm_manager.find_by_short_id(short_id)
    assert len(matches) == 1
    assert matches[0].name == "myvm"


def test_vm_manager_find_by_short_id_no_match(vm_manager: VMManager):
    assert vm_manager.find_by_short_id("zzzzzz") == []


def test_vm_manager_get_by_short_id_unique(vm_manager: VMManager):
    vm = VMInstance(name="uniquevm", pid=2, status=VMState.RUNNING)
    vm_manager.register(vm)
    registered = vm_manager.get("uniquevm")
    assert registered is not None
    result = vm_manager.get_by_short_id(registered.id[:6])
    assert result is not None
    assert result.name == "uniquevm"


def test_vm_manager_get_by_name_multiple(vm_manager: VMManager):
    vm1 = VMInstance(name="dup", pid=1, status=VMState.RUNNING)
    vm2 = VMInstance(name="dup", pid=2, status=VMState.RUNNING)
    vm_manager.register(vm1)
    vm_manager.register(vm2)
    results = vm_manager.get_by_name("dup")
    assert len(results) == 2


def test_vm_manager_update_status_success(vm_manager: VMManager):
    vm = VMInstance(name="statusvm", pid=3, status=VMState.RUNNING)
    vm_manager.register(vm)
    vm_manager.update_status("statusvm", VMState.STOPPED)
    updated = vm_manager.get("statusvm")
    assert updated is not None
    assert updated.status == VMState.STOPPED


def test_vm_manager_migration(tmp_path: Path):
    import json
    from datetime import datetime, timezone

    vms_dir = tmp_path / "vms"
    vms_dir.mkdir()
    state_file = vms_dir / "state.json"
    state_file.write_text(
        json.dumps(
            {
                "schema_version": 1,
                "vms": {
                    "mylegacyvm": {
                        "pid": 42,
                        "socket_path": None,
                        "ip": "10.0.0.2",
                        "mac": "02:FC:00:00:00:01",
                        "network_name": "default",
                        "tap_device": "mvm-tap0",
                        "created_at": datetime.now(tz=timezone.utc).isoformat(),
                        "status": "running",
                    }
                },
            }
        )
    )
    mgr = VMManager(vms_dir)
    vms = mgr.list_all()
    assert len(vms) == 1
    assert vms[0].name == "mylegacyvm"
    assert len(vms[0].id) == 64


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
    sample_vm.status = VMState.STOPPED

    from mvmctl.api.vms import get_vm_status_with_exit_code

    status, exit_code = get_vm_status_with_exit_code(sample_vm)

    # Verify returns sample_vm.status
    assert status == VMState.STOPPED


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
