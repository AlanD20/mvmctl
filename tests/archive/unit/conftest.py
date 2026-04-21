from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.archive.key_manager import KeyInfo
from mvmctl.core.archive.vm_manager import VMManager
from mvmctl.models.vm import VMConfig, VMInstance, VMStatus


@pytest.fixture(autouse=True)
def _mock_mvm_group_membership(request):
    """Auto-mock mvm group membership check for all unit tests.

    This prevents PrivilegeError from being raised in network operations
    when tests aren't specifically testing privilege behavior.

    Tests that need to verify privilege behavior should be marked with:
        @pytest.mark.real_mvm_group_check
    """
    if request.node.get_closest_marker("real_mvm_group_check"):
        yield None
        return

    with patch("mvmctl.utils.process.require_mvm_group_membership"):
        yield


@pytest.fixture
def mock_cache_dir(tmp_path: Path) -> Path:
    from tests.helpers.paths import make_test_paths

    paths = make_test_paths(tmp_path)
    cache_dir = paths.cache
    cache_dir.mkdir(parents=True, exist_ok=True)

    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = cache_dir / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    return cache_dir


@pytest.fixture
def mock_keys_dir(tmp_path: Path) -> Path:
    from tests.helpers.paths import make_test_paths

    cache_dir = make_test_paths(tmp_path).cache
    keys_dir = cache_dir / "keys"
    keys_dir.mkdir(parents=True, exist_ok=True)
    return keys_dir


@pytest.fixture
def sample_vm() -> VMInstance:
    """Return a sample VMInstance for use in tests."""
    return VMInstance(
        name="test-vm",
        id="testvm001abc1234",
        ipv4="10.20.0.2",
        mac="02:FC:aa:bb:cc:dd",
        pid=1234,
        status=VMStatus.RUNNING,
        created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        network_id="net-test-001",
        tap_device="mvm-tap0",
        rootfs_suffix=".ext4",
        kernel_id="kern-test-001",
        image_id="img-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
    )


@pytest.fixture
def stopped_vm() -> VMInstance:
    """Return a stopped VMInstance for use in tests."""
    return VMInstance(
        name="stopped-vm",
        id="stoppedvm01abc23",
        ipv4="10.20.0.3",
        mac="02:FC:11:22:33:44",
        pid=0,
        status=VMStatus.STOPPED,
        created_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        updated_at=datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        network_id="net-test-001",
        tap_device="mvm-tap0",
        rootfs_suffix=".ext4",
        kernel_id="kern-test-001",
        image_id="img-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
    )


@pytest.fixture
def make_test_vmconfig():
    """Factory fixture for creating VMConfig instances with sensible test defaults.

    Usage:
        def test_something(make_test_vmconfig):
            config = make_test_vmconfig(name="my-vm", vcpus=4)
            # config has all required fields populated with test-safe defaults
    """
    from mvmctl.models.cloudinit import CloudInitMode

    def _make(
        name: str = "test-vm",
        vcpu_count: int = 2,
        mem_size_mib: int = 512,
        disk_size_mib: int = 1024,
        kernel_path: Path | None = None,
        rootfs_path: Path | None = None,
        enable_api_socket: bool = True,
        enable_pci: bool = False,
        lsm_flags: str = "landlock,lockdown,yama,integrity,selinux,bpf",
        enable_logging: bool = True,
        enable_metrics: bool = False,
        enable_console: bool = True,
        cloud_init_mode=CloudInitMode.INJECT,
        **kwargs,
    ) -> VMConfig:
        # Default paths if not provided
        if kernel_path is None:
            kernel_path = Path("/tmp/vmlinux")
        if rootfs_path is None:
            rootfs_path = Path("/tmp/rootfs.ext4")

        return VMConfig(
            name=name,
            vcpu_count=vcpu_count,
            mem_size_mib=mem_size_mib,
            disk_size_mib=disk_size_mib,
            kernel_path=kernel_path,
            rootfs_path=rootfs_path,
            enable_api_socket=enable_api_socket,
            enable_pci=enable_pci,
            lsm_flags=lsm_flags,
            enable_logging=enable_logging,
            enable_metrics=enable_metrics,
            enable_console=enable_console,
            cloud_init_mode=cloud_init_mode,
            **kwargs,
        )

    return _make


@pytest.fixture
def vm_manager(tmp_path: Path) -> VMManager:
    return VMManager(tmp_path)


@pytest.fixture
def mock_subprocess_run_success(monkeypatch):
    """Shared fixture: mock subprocess.run to return success (returncode=0)."""
    mock = MagicMock(returncode=0, stdout="", stderr="")
    monkeypatch.setattr("subprocess.run", MagicMock(return_value=mock))
    return mock


@pytest.fixture
def mock_subprocess_run_failure(monkeypatch):
    """Shared fixture: mock subprocess.run to return failure (returncode=1)."""
    mock = MagicMock(returncode=1, stdout="", stderr="error")
    monkeypatch.setattr("subprocess.run", MagicMock(return_value=mock))
    return mock


@pytest.fixture
def running_vm(make_test_vmconfig) -> VMInstance:
    """VM in running state with all required config fields."""
    return VMInstance(
        name="running-vm",
        id="runningvm001abc5",
        ipv4="10.20.0.5",
        mac="02:FC:aa:bb:cc:01",
        pid=5678,
        status=VMStatus.RUNNING,
        created_at=datetime(2026, 1, 15, 8, 30, 0, tzinfo=timezone.utc),
        updated_at=datetime(2026, 1, 15, 8, 30, 0, tzinfo=timezone.utc),
        api_socket_path=Path("/tmp/running-vm.sock"),
        network_id="default",
        tap_device="mvm-tap0",
        rootfs_suffix=".ext4",
        kernel_id="kern-test-001",
        image_id="img-test-001",
        binary_id="bin-test-001",
        disk_size_mib=4096,
        config=make_test_vmconfig(name="running-vm", vcpu_count=4, mem_size_mib=4096),
    )


@pytest.fixture
def error_vm() -> VMInstance:
    """VM in error state."""
    return VMInstance(
        name="error-vm",
        id="errorvm001abc123",
        ipv4="10.20.0.6",
        mac="02:FC:aa:bb:cc:02",
        pid=0,
        status=VMStatus.ERROR,
        created_at=datetime(2026, 1, 15, 9, 0, 0, tzinfo=timezone.utc),
        updated_at=datetime(2026, 1, 15, 9, 0, 0, tzinfo=timezone.utc),
        network_id="net-test-001",
        tap_device="mvm-tap0",
        rootfs_suffix=".ext4",
        kernel_id="kern-test-001",
        image_id="img-test-001",
        binary_id="bin-test-001",
        disk_size_mib=1024,
    )


@pytest.fixture
def sample_network_config() -> dict:
    """Sample network configuration dict for tests."""
    return {
        "name": "default",
        "bridge": "mvm-br0",
        "subnet": "10.20.0.0/24",
        "ipv4_gateway": "10.20.0.1",
    }


@pytest.fixture
def sample_key_info() -> KeyInfo:
    """Sample KeyInfo for tests."""
    return KeyInfo(
        name="test-key",
        fingerprint="SHA256:abcdef1234567890",
        algorithm="ssh-ed25519",
        comment="testuser@testhost",
        added_at="2026-01-01T00:00:00+00:00",
    )


@pytest.fixture
def mock_tty(mocker):
    """Mock sys.stdout.isatty() return value.

    Returns a factory function that accepts is_tty parameter.
    Usage: mock_tty(is_tty=True) or mock_tty(is_tty=False)
    """

    def _mock_tty(is_tty: bool = True):
        return mocker.patch("sys.stdout.isatty", return_value=is_tty)

    return _mock_tty


@pytest.fixture
def mock_file_exists(mocker):
    """Mock Path.exists() return value.

    Returns a factory function that accepts exists parameter.
    Usage: mock_file_exists(exists=True) or mock_file_exists(exists=False)
    """

    def _mock_file_exists(exists: bool = True):
        return mocker.patch("pathlib.Path.exists", return_value=exists)

    return _mock_file_exists


@pytest.fixture
def mock_process_running(mocker):
    """Mock os.kill() for process existence check.

    Returns a factory function that accepts running parameter.
    Usage: mock_process_running(running=True) or mock_process_running(running=False)
    """

    def _mock_process_running(running: bool = True):
        if running:
            return mocker.patch("os.kill", return_value=None)
        else:
            return mocker.patch("os.kill", side_effect=ProcessLookupError())

    return _mock_process_running


@pytest.fixture
def mock_stat_size(mocker):
    """Mock Path.stat().st_size return value.

    Returns a factory function that accepts size_bytes parameter.
    Usage: mock_stat_size(size_bytes=1024)
    """

    def _mock_stat_size(size_bytes: int = 1024):
        mock_stat = mocker.MagicMock()
        mock_stat.st_size = size_bytes
        return mocker.patch("pathlib.Path.stat", return_value=mock_stat)

    return _mock_stat_size


@pytest.fixture(autouse=True)
def _mock_kernel_build_dependencies(request):
    if not request.node.name.startswith("test_build_kernel_pipeline"):
        yield None
        return

    with patch("mvmctl.api.kernel._check_build_dependencies", return_value=[]):
        yield
