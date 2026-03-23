import os
from pathlib import Path

import pytest


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
