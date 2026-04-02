from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core.key_manager import KeyInfo
from mvmctl.core.vm_manager import VMManager
from mvmctl.models.vm import VMConfig, VMInstance, VMState


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
def mock_cache_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Creates a mock cache directory with a fake kernel and image."""
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    return tmp_path


@pytest.fixture
def mock_keys_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Creates a mock keys directory."""
    keys_dir = tmp_path / "keys"
    keys_dir.mkdir(parents=True)
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    return keys_dir


@pytest.fixture
def sample_vm() -> VMInstance:
    """Return a sample VMInstance for use in tests."""
    return VMInstance(
        name="test-vm",
        ipv4="10.20.0.2",
        mac="02:FC:aa:bb:cc:dd",
        pid=1234,
        status=VMState.RUNNING,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
    )


@pytest.fixture
def stopped_vm() -> VMInstance:
    """Return a stopped VMInstance for use in tests."""
    return VMInstance(
        name="stopped-vm",
        ipv4="10.20.0.3",
        mac="02:FC:11:22:33:44",
        pid=None,
        status=VMState.STOPPED,
        created_at=datetime(2026, 1, 1, 12, 0, 0),
    )


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
def running_vm() -> VMInstance:
    """Running VM with all fields set."""
    return VMInstance(
        name="running-vm",
        ipv4="10.20.0.5",
        mac="02:FC:aa:bb:cc:01",
        pid=5678,
        status=VMState.RUNNING,
        created_at=datetime(2026, 1, 15, 8, 30, 0),
        api_socket_path=Path("/tmp/running-vm.sock"),
        network_name="default",
        config=VMConfig(name="running-vm", vcpu_count=4, mem_size_mib=4096),
    )


@pytest.fixture
def error_vm() -> VMInstance:
    """VM in error state."""
    return VMInstance(
        name="error-vm",
        ipv4="10.20.0.6",
        mac="02:FC:aa:bb:cc:02",
        pid=None,
        status=VMState.ERROR,
        created_at=datetime(2026, 1, 15, 9, 0, 0),
    )


@pytest.fixture
def sample_network_config() -> dict:
    """Sample network configuration dict for tests."""
    return {
        "name": "default",
        "bridge": "mvm-br0",
        "subnet": "10.20.0.0/24",
        "gateway": "10.20.0.1",
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

    with patch("mvmctl.core.kernel.check_build_dependencies", return_value=[]):
        yield
