from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from fcm.core.vm_manager import VMManager
from fcm.models.vm import VMInstance, VMState


@pytest.fixture
def mock_cache_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Creates a mock cache directory with a fake kernel and image."""
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    return tmp_path


@pytest.fixture
def mock_keys_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Creates a mock keys directory."""
    keys_dir = tmp_path / "keys"
    keys_dir.mkdir(parents=True)
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    return keys_dir


@pytest.fixture
def sample_vm() -> VMInstance:
    """Return a sample VMInstance for use in tests."""
    return VMInstance(
        name="test-vm",
        ip="10.20.0.2",
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
        ip="10.20.0.3",
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
    monkeypatch.setattr("subprocess.run", lambda *a, **kw: mock)
    return mock


@pytest.fixture
def mock_subprocess_run_failure(monkeypatch):
    """Shared fixture: mock subprocess.run to return failure (returncode=1)."""
    mock = MagicMock(returncode=1, stdout="", stderr="error")
    monkeypatch.setattr("subprocess.run", lambda *a, **kw: mock)
    return mock
