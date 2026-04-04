"""Integration test fixtures.

Note: Root conftest.py provides autouse fixtures that isolate MVM_CONFIG_DIR
and MVM_CACHE_DIR to tmp_path for all tests. These fixtures build on that.
"""

from pathlib import Path

import pytest


@pytest.fixture
def mock_cache_dir(tmp_path: Path) -> Path:
    from tests.helpers.paths import make_test_paths

    cache_dir = make_test_paths(tmp_path).cache
    cache_dir.mkdir(parents=True, exist_ok=True)

    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = cache_dir / "images"
    images_dir.mkdir(parents=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    return cache_dir
