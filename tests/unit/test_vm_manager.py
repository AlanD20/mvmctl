"""Tests for VM manager."""

from pathlib import Path
from datetime import datetime, timezone
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.vm_manager import VMManager
from mvmctl.models.vm import VMConfig, VMInstance, VMStatus
from mvmctl.models.cloud_init import CloudInitMode


@pytest.fixture
def setup_test_assets():
    """Create required database records (network, image, kernel, binary) for VM tests."""
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Binary, Image, Kernel, Network

    db = MVMDatabase()

    # Create test network
    network = Network(
        id="net-test-001",
        name="test-network",
        subnet="10.0.0.0/24",
        bridge="mvm-test-br0",
        ipv4_gateway="10.0.0.1",
        bridge_active=True,
        nat_enabled=True,
        is_default=False,
        created_at="2026-01-01T00:00:00",
        updated_at="2026-01-01T00:00:00",
    )
    db.upsert_network(network)

    # Create test image
    image = Image(
        id="img-test-001",
        os_slug="test-ubuntu",
        os_name="Test Ubuntu",
        arch="x86_64",
        path="/tmp/test-image.ext4",
        fs_type="ext4",
        fs_uuid="12345678-1234-1234-1234-123456789abc",
        minimum_rootfs_size_mib=1024,
        original_size=2147483648,
        pulled_at="2026-01-01T00:00:00",
        is_default=False,
        created_at="2026-01-01T00:00:00",
        updated_at="2026-01-01T00:00:00",
    )
    db.upsert_image(image)

    # Create test kernel
    kernel = Kernel(
        id="kern-test-001",
        name="test-vmlinux",
        base_name="vmlinux",
        version="5.10.0",
        arch="x86_64",
        type="firecracker",
        path="/tmp/test-vmlinux",
        is_default=False,
        created_at="2026-01-01T00:00:00",
        updated_at="2026-01-01T00:00:00",
    )
    db.upsert_kernel(kernel)

    # Create test binary
    binary = Binary(
        id="bin-test-001",
        name="firecracker",
        version="1.15.0",
        full_version="1.15.0",
        ci_version="1.15.0",
        path="/tmp/test-firecracker",
        is_default=False,
        created_at="2026-01-01T00:00:00",
        updated_at="2026-01-01T00:00:00",
    )
    db.upsert_binary(binary)

    return {
        "network_id": "net-test-001",
        "image_id": "img-test-001",
        "kernel_id": "kern-test-001",
        "binary_id": "bin-test-001",
    }


@pytest.mark.parametrize(
    "vm_name,pid,ipv4",
    [
        ("test-vm", 1234, "10.0.0.2"),
        ("my-vm", 5678, "10.0.0.5"),
        ("vm123", 9999, "192.168.1.10"),
    ],
)
def test_vm_manager_register(
    vm_manager: VMManager, setup_test_assets, vm_name: str, pid: int, ipv4: str
):
    """register should store a VMInstance that is retrievable by name with correct attributes."""
    assets = setup_test_assets
    vm = VMInstance(
        name=vm_name,
        id="vm001" + vm_name.replace("-", ""),
        pid=pid,
        ipv4=ipv4,
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        status=VMStatus.RUNNING,
        rootfs_suffix=".ext4",
        config=VMConfig(
            name=vm_name,
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )

    vm_manager.register(vm)

    retrieved = vm_manager.get(vm_name)
    assert retrieved is not None
    assert retrieved.name == vm_name
    assert retrieved.pid == pid
    assert retrieved.ipv4 == ipv4
    assert retrieved.status == VMStatus.RUNNING


def test_vm_manager_list(vm_manager: VMManager, setup_test_assets):
    """list_all should return all registered VMs."""
    assets = setup_test_assets
    vm1 = VMInstance(
        name="vm1",
        id="vm001abc12345678",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm1",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm2 = VMInstance(
        name="vm2",
        id="vm002def78901234",
        pid=2,
        status=VMStatus.STOPPED,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id=assets["network_id"],
        tap_device="mvm-tap1",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm2",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm1)
    vm_manager.register(vm2)

    vms = vm_manager.list_all()
    assert len(vms) == 2


def test_vm_manager_count_vms(vm_manager: VMManager, setup_test_assets):
    """count_vms should return the number of VMs without loading full metadata."""
    assets = setup_test_assets
    assert vm_manager.count_vms() == 0

    vm1 = VMInstance(
        name="vm1",
        id="vm001abc12345678",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm1",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm1)
    assert vm_manager.count_vms() == 1

    vm2 = VMInstance(
        name="vm2",
        id="vm002def78901234",
        pid=2,
        status=VMStatus.STOPPED,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id=assets["network_id"],
        tap_device="mvm-tap1",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm2",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm2)
    assert vm_manager.count_vms() == 2

    registered = vm_manager.get("vm1")
    assert registered is not None
    vm_manager.deregister(registered.id)
    assert vm_manager.count_vms() == 1


def test_vm_manager_deregister(vm_manager: VMManager, setup_test_assets):
    assets = setup_test_assets
    vm = VMInstance(
        name="test-vm",
        id="testvm001abc1234",
        pid=1234,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
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
    """update_status should not raise error when VM does not exist (no-op in DB)."""
    # update_status doesn't raise VMNotFoundError, it just calls DB update
    vm_manager.update_status(vm_name, new_status)
    # If we get here without exception, the test passes


def test_vm_manager_find_by_id_prefix(vm_manager: VMManager, setup_test_assets):
    assets = setup_test_assets
    vm = VMInstance(
        name="myvm",
        id="myvm001abc123456",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="myvm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm)
    registered = vm_manager.get("myvm")
    assert registered is not None
    prefix = registered.id[:6]
    matches = vm_manager.find_by_id_prefix(prefix)
    assert len(matches) == 1
    assert matches[0].name == "myvm"


def test_vm_manager_find_by_id_prefix_no_match(vm_manager: VMManager):
    assert vm_manager.find_by_id_prefix("zzzzzz") == []


def test_vm_manager_get_by_id_prefix_unique(vm_manager: VMManager, setup_test_assets):
    assets = setup_test_assets
    vm = VMInstance(
        name="uniquevm",
        id="uniquevm01abc234",
        pid=2,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="uniquevm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm)
    registered = vm_manager.get("uniquevm")
    assert registered is not None
    result = vm_manager.find_by_id_prefix(registered.id[:6])
    assert len(result) == 1
    vm_result = result[0]
    assert vm_result is not None
    assert vm_result.name == "uniquevm"


def test_vm_manager_get_by_full_id_exact_match(vm_manager: VMManager, setup_test_assets):
    """Test that get returns VM by name and ID matches."""
    assets = setup_test_assets
    vm = VMInstance(
        name="testvm",
        id="testvm001abc5678",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="testvm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm)
    registered = vm_manager.get("testvm")
    assert registered is not None

    # Verify the VM was stored correctly
    assert registered.name == "testvm"
    assert registered.id == "testvm001abc5678"


def test_vm_manager_get_by_full_id_no_match(vm_manager: VMManager):
    """Test that get returns None for non-existent VM."""
    result = vm_manager.get("nonexistent-vm")
    assert result is None


def test_vm_manager_get_by_full_id_collision_resistance(vm_manager: VMManager, setup_test_assets):
    """Test that find_by_id_prefix returns multiple VMs with same prefix."""
    assets = setup_test_assets
    vm1 = VMInstance(
        name="vm1",
        id="abc123aaaaaaaaaa",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm1",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm2 = VMInstance(
        name="vm2",
        id="abc123bbbbbbbbbb",
        pid=2,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id=assets["network_id"],
        tap_device="mvm-tap1",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="vm2",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )

    vm_manager.register(vm1)
    vm_manager.register(vm2)

    # find_by_id_prefix should return both VMs (ambiguous prefix)
    prefix_result = vm_manager.find_by_id_prefix("abc123")
    assert len(prefix_result) == 2

    # get should return correct VM for each name
    result1 = vm_manager.get("vm1")
    assert result1 is not None
    assert result1.name == "vm1"

    result2 = vm_manager.get("vm2")
    assert result2 is not None
    assert result2.name == "vm2"


def test_vm_manager_get_by_name_returns_single(vm_manager: VMManager, setup_test_assets):
    assets = setup_test_assets
    vm = VMInstance(
        name="dup",
        id="dupvm001abc12345",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="dup",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm)
    results = vm_manager.get_by_name("dup")
    assert len(results) == 1
    assert results[0].name == "dup"


def test_vm_manager_update_status_success(vm_manager: VMManager, setup_test_assets):
    assets = setup_test_assets
    vm = VMInstance(
        name="statusvm",
        id="statusvm01abc345",
        pid=3,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="statusvm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    vm_manager.register(vm)
    # update_status takes vm_id, not name
    registered = vm_manager.get("statusvm")
    assert registered is not None
    vm_manager.update_status(registered.id, VMStatus.STOPPED)
    updated = vm_manager.get("statusvm")
    assert updated is not None
    assert updated.status == VMStatus.STOPPED


def test_vm_manager_persists_to_sqlite(tmp_path: Path, setup_test_assets):
    """VMManager persists VMs to SQLite database."""
    assets = setup_test_assets
    mgr = VMManager(tmp_path)
    vm = VMInstance(
        name="testvm",
        id="testvm001def5678",
        pid=42,
        ipv4="10.0.0.2",
        status=VMStatus.RUNNING,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        mac="02:FC:00:00:00:01",
        network_id=assets["network_id"],
        tap_device="mvm-tap0",
        image_id=assets["image_id"],
        kernel_id=assets["kernel_id"],
        binary_id=assets["binary_id"],
        disk_size_mib=1024,
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="testvm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
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


# ---------------------------------------------------------------------------
# _vm_instance_to_db_state branch coverage
# ---------------------------------------------------------------------------


def test_vm_instance_to_db_state_with_network_name(vm_manager: VMManager):
    """network_id is passed explicitly and used directly."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="netvm",
        id="netvm001abc12345",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        network_id="net-test-001",
        tap_device="mvm-tap0",
        image_id="img-test-001",
        kernel_id="kern-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.network_id == "net-test-001"


def test_vm_instance_to_db_state_network_name_not_found(vm_manager: VMManager):
    """network_id is passed explicitly and used directly."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="netvm2",
        id="netvm002def56789",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.3",
        mac="02:FC:00:00:00:02",
        network_id="",
        tap_device="mvm-tap0",
        image_id="img-test-001",
        kernel_id="kern-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.network_id == ""


def test_vm_instance_to_db_state_image_id_prefix_match(vm_manager: VMManager):
    """image_id is passed explicitly and used directly."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="imgvm",
        id="imgvm001abc12345",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.4",
        mac="02:FC:00:00:00:03",
        network_id="net-test-001",
        tap_device="mvm-tap0",
        image_id="abc123",
        kernel_id="kern-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.image_id == "abc123"


def test_vm_instance_to_db_state_image_id_slug_fallback(vm_manager: VMManager):
    """image_id is passed explicitly and used directly."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="imgvm2",
        id="imgvm002def56789",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.5",
        mac="02:FC:00:00:00:04",
        network_id="net-test-001",
        tap_device="mvm-tap0",
        image_id="ubuntu-24.04",
        kernel_id="kern-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.image_id == "ubuntu-24.04"


def test_vm_instance_to_db_state_kernel_id_prefix_match(vm_manager: VMManager):
    """kernel_id is passed explicitly and used directly."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="kervm",
        id="kervm001abc12345",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.6",
        mac="02:FC:00:00:00:05",
        network_id="net-test-001",
        tap_device="mvm-tap0",
        image_id="img-test-001",
        kernel_id="ker123",
        binary_id="bin-test-001",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.kernel_id == "ker123"


def test_vm_instance_to_db_state_binary_id_from_vm(vm_manager: VMManager):
    """binary_id is extracted from VMInstance (Resolution Layer Mandate)."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _vm_instance_to_db_state

    mock_db = MagicMock()

    vm = VMInstance(
        name="binvm",
        id="binvm001abc12345",
        pid=1,
        status=VMStatus.RUNNING,
        ipv4="10.0.0.7",
        mac="02:FC:00:00:00:06",
        network_id="net-test-001",
        tap_device="mvm-tap0",
        image_id="img-test-001",
        kernel_id="kern-test-001",
        binary_id="bin-full-id",
        disk_size_mib=1024,
        created_at=datetime.now(tz=timezone.utc),
        updated_at=datetime.now(tz=timezone.utc),
        rootfs_suffix=".ext4",
        config=VMConfig(
            name="test-vm",
            vcpu_count=2,
            mem_size_mib=512,
            disk_size_mib=1024,
            lsm_flags="",
            rootfs_path=Path("/tmp/rootfs.ext4"),
            cloud_init_mode=CloudInitMode.INJECT,
            enable_api_socket=True,
            enable_pci=False,
            enable_logging=True,
            enable_metrics=False,
            enable_console=True,
        ),
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _vm_instance_to_db_state(vm)

    assert result.binary_id == "bin-full-id"


# ---------------------------------------------------------------------------
# _db_state_to_vm_instance branch coverage
# ---------------------------------------------------------------------------


def test_db_state_to_vm_instance_network_id_resolves_name():
    """network_id is correctly populated from DB state."""
    from unittest.mock import MagicMock, patch

    from mvmctl.core.vm_manager import _db_state_to_vm_instance
    from mvmctl.db.models import VMInstance as DBVMInstance

    mock_db = MagicMock()

    state = DBVMInstance(
        id="abc123def456abcd",
        name="testvm",
        status="running",
        network_id="some-net-id",
        pid=1234,
        ipv4="10.0.0.2",
        mac="02:FC:00:00:00:01",
        tap_device="mvm-tap0",
        image_id="img-abc123",
        kernel_id="kern-abc123",
        binary_id="bin-abc123",
        api_socket_path=None,
        console_socket_path=None,
        config_path="/tmp/config.json",
        cloud_init_mode="inject",
        nocloud_net_port=None,
        nocloud_server_pid=None,
        console_relay_pid=None,
        exit_code=None,
        vcpu_count=2,
        mem_size_mib=512,
        disk_size_mib=1024,
        rootfs_path="/tmp/rootfs.ext4",
        rootfs_suffix=".ext4",
        enable_api_socket=True,
        enable_pci=False,
        enable_logging=True,
        enable_metrics=False,
        enable_console=True,
        created_at="2026-01-01T00:00:00",
        updated_at="2026-01-01T00:00:00",
    )
    with patch("mvmctl.core.mvm_db.MVMDatabase", return_value=mock_db):
        result = _db_state_to_vm_instance(state)

    assert result.network_id == "some-net-id"
