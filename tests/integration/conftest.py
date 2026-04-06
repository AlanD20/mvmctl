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


@pytest.fixture
def seed_test_assets(tmp_path: Path) -> Path:
    """Seed database with test image and kernel entries."""
    from tests.helpers.paths import make_test_paths
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Image, Kernel

    paths = make_test_paths(tmp_path)
    cache_dir = paths.cache
    cache_dir.mkdir(parents=True, exist_ok=True)

    db = MVMDatabase()

    db.upsert_kernel(
        Kernel(
            id="a" * 64,
            name="vmlinux-test",
            version="6.1.0",
            arch="x86_64",
            path="vmlinux",
            base_name="vmlinux",
            type="official",
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )

    db.upsert_image(
        Image(
            id="b" * 64,
            os_slug="ubuntu-24.04",
            arch="x86_64",
            path="ubuntu-24.04.ext4",
            os_name="Ubuntu 24.04",
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )

    return cache_dir
