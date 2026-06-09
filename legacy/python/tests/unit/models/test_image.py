"""Tests for ImageItem dataclass — construction and field defaults."""

from __future__ import annotations

from datetime import UTC, datetime

from mvmctl.models import ImageItem


def _make_image(
    type: str = "ubuntu-24.04",
    name: str = "Ubuntu 24.04",
    distro: str | None = None,
) -> ImageItem:
    ts = datetime.now(tz=UTC).isoformat()
    return ImageItem(
        id="a" * 64,
        type=type,
        name=name,
        arch="x86_64",
        path="ubuntu-24.04.ext4",
        fs_type="ext4",
        minimum_rootfs_size_mib=2048,
        original_size=2147483648,
        is_default=False,
        is_present=True,
        pulled_at=ts,
        created_at=ts,
        updated_at=ts,
        distro=distro,
    )


class TestImageItemModel:
    """Tests for ImageItem dataclass creation and defaults."""

    def test_create_without_distro(self) -> None:
        """ImageItem can be created without distro, defaults to None."""
        img = _make_image()
        assert img.distro is None

    def test_create_with_distro(self) -> None:
        """ImageItem can be created with distro set."""
        img = _make_image(distro="ubuntu")
        assert img.distro == "ubuntu"

    def test_create_with_alpine_distro(self) -> None:
        """ImageItem with alpine distro."""
        img = _make_image(
            type="alpine-3.21", name="Alpine 3.21", distro="alpine"
        )
        assert img.distro == "alpine"
        assert img.type == "alpine-3.21"

    def test_create_with_debian_distro(self) -> None:
        """ImageItem with debian distro."""
        img = _make_image(type="debian-12", name="Debian 12", distro="debian")
        assert img.distro == "debian"
        assert img.type == "debian-12"

    def test_create_with_empty_distro_string(self) -> None:
        """ImageItem with empty string distro (treated as set but empty)."""
        img = _make_image(distro="")
        assert img.distro == ""

    def test_distro_default_field_value(self) -> None:
        """ImageItem default for distro is None when not provided."""
        # bypass _make_image, construct directly
        ts = datetime.now(tz=UTC).isoformat()
        img = ImageItem(
            id="b" * 64,
            type="fedora-40",
            name="Fedora 40",
            arch="x86_64",
            path="fedora-40.ext4",
            fs_type="ext4",
            minimum_rootfs_size_mib=2048,
            original_size=2147483648,
            is_default=False,
            is_present=True,
            pulled_at=ts,
            created_at=ts,
            updated_at=ts,
        )
        assert img.distro is None
