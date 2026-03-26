"""Integration test fixtures.

Note: Root conftest.py provides autouse fixtures that isolate MVM_CONFIG_DIR
and MVM_CACHE_DIR to tmp_path for all tests. These fixtures build on that.
"""

from pathlib import Path

import pytest


@pytest.fixture
def mock_cache_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Creates a mock cache directory with standard subdirectories.

    Sets MVM_CACHE_DIR to tmp_path and creates common subdirectories.
    Returns the cache directory path for tests that need to interact
    with metadata directly.
    """
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = tmp_path / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    return tmp_path
