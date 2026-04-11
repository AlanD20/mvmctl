"""Integration test fixtures.

Note: Root conftest.py provides autouse fixtures that isolate MVM_CONFIG_DIR
and MVM_CACHE_DIR to tmp_path for all tests. These fixtures build on that.
"""

from pathlib import Path
from unittest.mock import patch

import pytest


@pytest.fixture(autouse=True)
def _seed_test_image_for_all_integration_tests(tmp_path: Path):
    """Auto-seed test image with minimum_rootfs_size_mib for all integration tests.

    This prevents VMCreateError from being raised due to missing minimum_rootfs_size_mib.
    """
    from tests.helpers.paths import make_test_paths
    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Image

    paths = make_test_paths(tmp_path)
    cache_dir = paths.cache
    cache_dir.mkdir(parents=True, exist_ok=True)

    # Create fake image file
    images_dir = cache_dir / "images"
    images_dir.mkdir(parents=True, exist_ok=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    db = MVMDatabase()
    db.upsert_image(
        Image(
            id="b" * 64,
            os_slug="ubuntu-24.04",
            arch="x86_64",
            path="ubuntu-24.04.ext4",
            os_name="Ubuntu 24.04",
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789abc",
            minimum_rootfs_size_mib=2048,
            original_size=2147483648,
            is_default=False,
            pulled_at="2026-01-01T00:00:00+00:00",
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )

    yield


@pytest.fixture
def mock_cache_dir(tmp_path: Path) -> Path:
    from tests.helpers.paths import make_test_paths

    cache_dir = make_test_paths(tmp_path).cache
    cache_dir.mkdir(parents=True, exist_ok=True)

    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    images_dir = cache_dir / "images"
    images_dir.mkdir(parents=True, exist_ok=True)
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
            name="vmlinux",
            version="6.1.0",
            arch="x86_64",
            path="vmlinux",
            base_name="vmlinux",
            type="official",
            is_default=False,
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
            fs_type="ext4",
            fs_uuid="12345678-1234-1234-1234-123456789abc",
            minimum_rootfs_size_mib=2048,
            original_size=2147483648,
            is_default=False,
            pulled_at="2026-01-01T00:00:00+00:00",
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )

    return cache_dir
