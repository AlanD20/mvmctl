"""Tests for CLI asset commands (kernel, image, bin, cache clear)."""

import hashlib
import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from click.testing import CliRunner as ClickCliRunner
from typer.testing import CliRunner

from mvmctl.cli.bin import kernel_app
from mvmctl.core.binary_manager import BinaryVersion
from mvmctl.core.image import ImageImportResult
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Binary, Image, Kernel
from mvmctl.exceptions import AssetNotFoundError, BinaryError, KernelError
from mvmctl.main import app as main_app
from mvmctl.models.image import ImageSpec
from mvmctl.models.kernel import KernelSpec

runner = CliRunner()
click_runner = ClickCliRunner()


def _seed_kernel_db(cache_dir: Path, kernel_name: str = "vmlinux") -> None:
    """Seed SQLite database with a kernel entry for testing."""
    from datetime import datetime, timezone

    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Kernel

    db = MVMDatabase(cache_dir / "mvmdb.db")
    db.migrate()
    kernel_entry = Kernel(
        id="a" * 16,
        name=kernel_name,
        base_name=kernel_name,
        version="-",
        arch="-",
        type="official",
        path=f"kernels/{kernel_name}",
        is_default=False,
        created_at=datetime.now(timezone.utc).isoformat(),
        updated_at=datetime.now(timezone.utc).isoformat(),
    )
    db.upsert_kernel(kernel_entry)


def _seed_image_db(cache_dir: Path, image_id: str = "test-image") -> None:
    """Seed SQLite database with an image entry for testing."""
    from datetime import datetime, timezone

    from mvmctl.core.mvm_db import MVMDatabase
    from mvmctl.db.models import Image

    db = MVMDatabase(cache_dir / "mvmdb.db")
    db.migrate()
    image_entry = Image(
        id=image_id,
        os_slug="test-os",
        os_name="Test OS",
        path=f"images/{image_id}.ext4",
        fs_type="ext4",
        fs_uuid=None,
        compressed_size=None,
        original_size=None,
        compression_ratio=None,
        compressed_format=None,
        pulled_at=None,
        is_default=False,
        created_at=datetime.now(timezone.utc).isoformat(),
        updated_at=datetime.now(timezone.utc).isoformat(),
    )
    db.upsert_image(image_entry)


_FAKE_IMAGES = [
    ImageSpec(
        id="ubuntu-24.04",
        image_type="ubuntu",
        version="24.04",
        name="Ubuntu 24.04 LTS",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256=None,
    ),
    ImageSpec(
        id="debian-12",
        image_type="debian",
        version="12",
        name="Debian 12",
        source="https://example.com/debian.qcow2",
        format="qcow2",
        convert_to="ext4",
        minimum_rootfs_size=2048,
        sha256=None,
    ),
]


# ---------------------------------------------------------------------------
# kernel ls
# ---------------------------------------------------------------------------


def test_kernel_ls_normal(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * (1024 * 1024))
    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(exist_ok=True)
    kernel_in_kernels = kernels_dir / "vmlinux"
    kernel_in_kernels.write_bytes(b"\x00" * (1024 * 1024))

    _seed_kernel_db(cache_dir, "vmlinux")

    import os

    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(kernels_dir)])
    assert result.exit_code == 0
    assert "vmlinux" in result.output


def test_kernel_ls_empty_dir(tmp_path: Path):
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path)])
    assert result.exit_code == 0


def test_kernel_ls_json(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * 2048)
    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir(exist_ok=True)
    kernel_in_kernels = kernels_dir / "vmlinux"
    kernel_in_kernels.write_bytes(b"\x00" * 2048)

    _seed_kernel_db(cache_dir, "vmlinux")

    import os

    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(kernels_dir), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    assert len(data) == 1
    assert data[0]["name"] == "vmlinux"
    assert "version" in data[0]
    assert "type" in data[0]


def test_kernel_ls_dir_not_found(tmp_path: Path):
    missing = tmp_path / "nope"
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(missing)])
    assert result.exit_code == 0
    assert missing.exists()


def test_kernel_ls_multiple_files(tmp_path: Path):
    import os

    (tmp_path / "vmlinux").write_bytes(b"\x00" * 1024)
    (tmp_path / "vmlinux-6.1.102").write_bytes(b"\x00" * 2048)
    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    _write_kernel_meta(cache_dir, "a" * 64, "vmlinux")
    _write_kernel_meta(cache_dir, "b" * 64, "vmlinux-6.1.102")
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = [e["name"] for e in data]
    assert "vmlinux" in names
    assert len(data) == 2
    full_names = [e["full_name"] for e in data]
    assert "vmlinux" in full_names
    assert "vmlinux-6.1.102" in full_names


def test_kernel_ls_skips_non_vmlinux_files(tmp_path: Path):
    import os

    (tmp_path / "vmlinux").write_bytes(b"\x00" * 1024)
    (tmp_path / "somefile.txt").write_text("not a kernel")
    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    _write_kernel_meta(cache_dir, "a" * 64, "vmlinux")
    _write_kernel_meta(cache_dir, "b" * 64, "vmlinux-valid")
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = [e["name"] for e in data]
    assert "vmlinux" in names
    assert "somefile.txt" not in names


# ---------------------------------------------------------------------------
# kernel fetch
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_success(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    out = tmp_path / "vmlinux-6.1.9"
    out.write_bytes(b"\x7fELF")
    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--type", "official", "--version", "6.1.9", "--out", str(out)]
    )
    assert result.exit_code == 0
    assert "Kernel" in result.output
    mock_build.assert_called_once()


@patch("mvmctl.cli.bin.build_kernel_pipeline", side_effect=KernelError("build failed"))
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_failure(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    out = tmp_path / "vmlinux-6.1.9"
    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--type", "official", "--version", "6.1.9", "--out", str(out)]
    )
    assert result.exit_code == 1


@patch("mvmctl.cli.bin.download_firecracker_kernel")
@patch("mvmctl.cli.bin._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_firecracker_success(
    mock_resolve: MagicMock, mock_ci: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    mock_resolve.return_value = KernelSpec(
        name="kernel-firecracker",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.com/fc/{ci_version}/{arch}/vmlinux-{version}",
        output_name="vmlinux-fc",
        build_dir="/tmp/build",
        list_url_template="https://example.com/list?ci={ci_version}&arch={arch}",
    )
    fc_kernel = tmp_path / "vmlinux-fc-1.12-amd64"
    fc_kernel.write_bytes(b"\x7fELF")
    mock_dl.return_value = fc_kernel
    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--type", "firecracker", "--version", "1.12"],
    )
    assert result.exit_code == 0
    assert "ready" in result.output.lower() or "kernel" in result.output.lower()
    call_kwargs = mock_dl.call_args.kwargs
    assert call_kwargs["output_name"] is None
    assert call_kwargs["output_path"] is None


@patch("mvmctl.cli.bin.download_firecracker_kernel")
@patch("mvmctl.cli.bin._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_firecracker_flag_shortcut(
    mock_resolve: MagicMock, mock_ci: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    mock_resolve.return_value = KernelSpec(
        name="kernel-firecracker",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.com/fc/{ci_version}/{arch}/vmlinux-{version}",
        output_name="vmlinux-fc",
        build_dir="/tmp/build",
        list_url_template="https://example.com/list?ci={ci_version}&arch={arch}",
    )
    fc_kernel = tmp_path / "vmlinux-fc"
    fc_kernel.write_bytes(b"\x7fELF")
    mock_dl.return_value = fc_kernel

    result = click_runner.invoke(main_app, ["kernel", "fetch", "--firecracker"])

    assert result.exit_code == 0
    mock_resolve.assert_called_once_with(kernel_type="firecracker", version=None)


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_flag_shortcut(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    out = tmp_path / "vmlinux-6.1.9"
    out.write_bytes(b"\x7fELF")
    result = click_runner.invoke(main_app, ["kernel", "fetch", "--official", "--out", str(out)])

    assert result.exit_code == 0
    mock_resolve.assert_called_once_with(kernel_type="official", version=None)


def test_kernel_fetch_official_conflicts_with_firecracker():
    result = click_runner.invoke(main_app, ["kernel", "fetch", "--firecracker", "--official"])
    assert result.exit_code == 1
    assert "cannot be combined" in result.output


def test_kernel_fetch_name_conflicts_with_out(tmp_path: Path):
    out = tmp_path / "vmlinux-custom"
    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--official", "--name", "custom-kernel", "--out", str(out)],
    )
    assert result.exit_code == 1
    assert "--name cannot be combined with --out" in result.output


@patch("mvmctl.cli.bin.download_firecracker_kernel")
@patch("mvmctl.cli.bin._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_firecracker_uses_name_override(
    mock_resolve: MagicMock, mock_ci: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    mock_resolve.return_value = KernelSpec(
        name="kernel-firecracker",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.com/fc/{ci_version}/{arch}/vmlinux-{version}",
        output_name="vmlinux-fc",
        build_dir="/tmp/build",
        list_url_template="https://example.com/list?ci={ci_version}&arch={arch}",
    )
    result_path = tmp_path / "my-fc-kernel"
    result_path.write_bytes(b"\x7fELF")
    mock_dl.return_value = result_path

    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--firecracker", "--name", "my-fc-kernel"]
    )

    assert result.exit_code == 0
    assert mock_dl.call_args.kwargs["output_name"] == "my-fc-kernel"
    assert mock_dl.call_args.kwargs["output_path"] is None


@patch("mvmctl.cli.bin.download_firecracker_kernel")
@patch("mvmctl.cli.bin._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_firecracker_out_is_explicit_path(
    mock_resolve: MagicMock, mock_ci: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    mock_resolve.return_value = KernelSpec(
        name="kernel-firecracker",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.com/fc/{ci_version}/{arch}/vmlinux-{version}",
        output_name="vmlinux-fc",
        build_dir="/tmp/build",
        list_url_template="https://example.com/list?ci={ci_version}&arch={arch}",
    )
    explicit_out = tmp_path / "explicit-kernel-path"
    explicit_out.write_bytes(b"\x7fELF")
    mock_dl.return_value = explicit_out

    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--firecracker", "--out", str(explicit_out)],
    )

    assert result.exit_code == 0
    assert mock_dl.call_args.kwargs["output_name"] is None
    assert mock_dl.call_args.kwargs["output_path"] == explicit_out


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_uses_name_override(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--official", "--name", "my-official-kernel"]
    )

    assert result.exit_code == 0
    output_path = mock_build.call_args.kwargs["output_path"]
    assert output_path.name.startswith("my-official-kernel-6.1.9-")
    assert mock_build.call_args.kwargs["use_cache"] is True


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_without_name_uses_cache(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    result = click_runner.invoke(main_app, ["kernel", "fetch", "--official"])

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["use_cache"] is True


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_clean_build_disables_cache(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    result = click_runner.invoke(main_app, ["kernel", "fetch", "--official", "--clean-build"])

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["use_cache"] is False


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_official_name_with_clean_build_disables_cache(
    mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path
):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )
    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--official", "--name", "custom-base", "--clean-build"],
    )

    assert result.exit_code == 0
    assert mock_build.call_args.kwargs["use_cache"] is False


def test_kernel_fetch_requires_type_or_shortcut():
    result = click_runner.invoke(main_app, ["kernel", "fetch"])
    assert result.exit_code == 1
    assert "Provide --type" in result.output


def test_kernel_fetch_firecracker_conflicting_type():
    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--firecracker", "--type", "official"],
    )
    assert result.exit_code == 1
    assert "cannot be combined" in result.output


@patch("mvmctl.cli.bin.resolve_kernel_spec", side_effect=KernelError("ambiguous type"))
def test_kernel_fetch_type_ambiguity_error(mock_resolve: MagicMock):
    result = click_runner.invoke(main_app, ["kernel", "fetch", "--type", "firecracker"])
    assert result.exit_code == 1
    assert "ambiguous type" in result.output


@patch("mvmctl.cli.bin.build_kernel_pipeline")
@patch("mvmctl.cli.bin.resolve_kernel_spec")
def test_kernel_fetch_with_jobs(mock_resolve: MagicMock, mock_build: MagicMock, tmp_path: Path):
    from mvmctl.core.kernel import KernelPipelineResult

    mock_resolve.return_value = KernelSpec(
        name="kernel-official",
        kernel_type="official",
        version="6.1.9",
        source="https://example.com/linux-6.1.9.tar.xz",
        output_name="vmlinux-official",
        build_dir="/tmp/build",
    )

    mock_result = MagicMock(spec=KernelPipelineResult)
    mock_result.build_dir = tmp_path / "build"
    mock_result.config_result = None
    mock_result.build_result = None
    mock_build.return_value = mock_result

    out = tmp_path / "vmlinux-6.1.9"
    out.write_bytes(b"\x7fELF")
    result = click_runner.invoke(
        main_app,
        [
            "kernel",
            "fetch",
            "--type",
            "official",
            "--version",
            "6.1.9",
            "-j",
            "4",
            "--out",
            str(out),
        ],
    )
    assert result.exit_code == 0
    mock_build.assert_called_once()


# ---------------------------------------------------------------------------
# kernel rm
# ---------------------------------------------------------------------------


def _write_kernel_meta(cache_dir: Path, full_hash: str, filename: str, **extra: object) -> None:
    """Seed a kernel entry in the SQLite database."""
    db = MVMDatabase()
    db.migrate()
    db.upsert_kernel(
        Kernel(
            id=full_hash,
            name=filename,
            version=str(extra.get("version", "6.1.9")),
            arch=str(extra.get("arch", "x86_64")),
            path=filename,
            base_name=extra.get("base_name"),
            type=extra.get("type", "firecracker"),
            is_default=bool(extra.get("is_default", False)),
            created_at=extra.get("created_at"),
            updated_at=extra.get("updated_at", "2026-01-01T12:00:00+00:00"),
        )
    )


def test_kernel_rm_success(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    full_hash = "a" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(cache_dir, full_hash, kernel.name)
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not kernel.exists()


def test_kernel_rm_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", "abcdef", "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# image ls
# ---------------------------------------------------------------------------


def test_image_ls_normal(tmp_path: Path):
    (tmp_path / "ubuntu-24.04.ext4").write_bytes(b"\x00" * 1024)
    (tmp_path / "debian-12.ext4").write_bytes(b"\x00" * 1024)
    with (
        patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.bin.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "Ubuntu 24.04 LTS" in result.output
    assert "Debian 12" in result.output


def test_image_ls_json():
    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    names = {item["name"] for item in data}
    assert "Ubuntu 24.04 LTS" in names or len(data) == 0


def test_image_ls_empty():
    with patch("mvmctl.cli.bin.load_images_config", return_value=[]):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data == []


def test_image_ls_shows_cached_marker(tmp_path: Path):
    (tmp_path / "ubuntu-24.04.ext4").touch()
    with (
        patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.bin.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0


# ---------------------------------------------------------------------------
# image fetch
# ---------------------------------------------------------------------------


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.bin._save_image_meta")
@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_success(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    mock_save_meta: MagicMock,
    mock_read_bytes: MagicMock,
    tmp_path: Path,
):
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4", fs_type="ext4", fs_uuid="test-uuid"
    )
    result = click_runner.invoke(
        main_app, ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)]
    )
    assert result.exit_code == 0
    assert "Image ready" in result.output
    mock_fetch.assert_called_once()


@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_not_found(mock_config: MagicMock):
    result = click_runner.invoke(main_app, ["image", "fetch", "nonexistent"])
    assert result.exit_code == 1


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.bin._save_image_meta")
@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config")
def test_image_fetch_by_type_and_version(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    mock_save_meta: MagicMock,
    mock_read_bytes: MagicMock,
    tmp_path: Path,
):
    mock_config.return_value = [
        ImageSpec(
            id="ubuntu-22.04",
            image_type="ubuntu",
            version="22.04",
            name="Ubuntu 22.04 LTS",
            source="https://example.com/ubuntu-22.04.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256=None,
        ),
        ImageSpec(
            id="ubuntu-24.04",
            image_type="ubuntu",
            version="24.04",
            name="Ubuntu 24.04 LTS",
            source="https://example.com/ubuntu-24.04.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256=None,
        ),
    ]
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4", fs_type="ext4", fs_uuid="test-uuid"
    )

    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu", "--version", "24.04", "--out", str(tmp_path)],
    )
    assert result.exit_code == 0
    called_spec = mock_fetch.call_args.args[0]
    assert called_spec.id == "ubuntu-24.04"


@patch("mvmctl.cli.bin.load_images_config")
def test_image_fetch_type_ambiguous_requires_version(mock_config: MagicMock):
    mock_config.return_value = [
        ImageSpec(
            id="ubuntu-22.04",
            image_type="ubuntu",
            version="22.04",
            name="Ubuntu 22.04 LTS",
            source="https://example.com/ubuntu-22.04.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256=None,
        ),
        ImageSpec(
            id="ubuntu-24.04",
            image_type="ubuntu",
            version="24.04",
            name="Ubuntu 24.04 LTS",
            source="https://example.com/ubuntu-24.04.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256=None,
        ),
    ]

    result = click_runner.invoke(main_app, ["image", "fetch", "ubuntu"])
    assert result.exit_code == 1
    assert "Provide --version" in result.output


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.bin._save_image_meta")
@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config")
def test_image_fetch_with_type_option(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    mock_save_meta: MagicMock,
    mock_read_bytes: MagicMock,
    tmp_path: Path,
):
    mock_config.return_value = [
        ImageSpec(
            id="ubuntu-24.04",
            image_type="ubuntu",
            version="24.04",
            name="Ubuntu 24.04 LTS",
            source="https://example.com/ubuntu-24.04.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256=None,
        )
    ]
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4", fs_type="ext4", fs_uuid="test-uuid"
    )

    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu", "--type", "ubuntu", "--version", "24.04"],
    )
    assert result.exit_code == 0


@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_type_option_conflicts_with_id(mock_config: MagicMock):
    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu-24.04", "--type", "ubuntu"],
    )
    assert result.exit_code == 1
    assert "cannot be used when selector is an image ID" in result.output


@patch("mvmctl.cli.bin.fetch_image", return_value=None)
@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_failure(mock_config: MagicMock, mock_fetch: MagicMock, tmp_path: Path):
    result = click_runner.invoke(
        main_app, ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)]
    )
    assert result.exit_code == 1


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.bin._save_image_meta")
@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_with_force(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    mock_save_meta: MagicMock,
    mock_read_bytes: MagicMock,
    tmp_path: Path,
):
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4", fs_type="ext4", fs_uuid="test-uuid"
    )
    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path), "--force"],
    )
    assert result.exit_code == 0
    call_args = mock_fetch.call_args
    assert (
        call_args[0][2] is True
        or call_args.kwargs.get("force") is True
        or call_args[1].get("force") is True
    )


@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_saves_fs_uuid_in_metadata(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    image_path = tmp_path / "images" / "ubuntu-24.04.ext4"
    image_path.parent.mkdir(parents=True, exist_ok=True)
    image_path.write_bytes(b"image")
    # fs_uuid is now returned in ImageImportResult from core layer
    mock_fetch.return_value = ImageImportResult(
        path=image_path, fs_type="ext4", fs_uuid="aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    )

    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu-24.04", "--out", str(image_path.parent), "--force"],
    )

    assert result.exit_code == 0
    db = MVMDatabase()
    db.migrate()
    images = db.list_images()
    assert len(images) > 0
    img = images[0]
    assert img.fs_uuid is not None
    assert len(img.fs_uuid) > 0


@patch("mvmctl.cli.bin.import_image")
def test_image_import_saves_fs_uuid_in_metadata(
    mock_import: MagicMock,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    source = tmp_path / "source.qcow2"
    source.write_bytes(b"qcow2")
    imported = tmp_path / "images" / "imported.ext4"
    imported.parent.mkdir(parents=True, exist_ok=True)
    imported.write_bytes(b"image")
    # fs_uuid is now returned in ImageImportResult from core layer
    mock_import.return_value = ImageImportResult(
        path=imported, fs_type="ext4", fs_uuid="ffffffff-1111-2222-3333-444444444444"
    )

    result = click_runner.invoke(
        main_app,
        [
            "image",
            "import",
            "Imported OS",
            str(source),
            "--format",
            "qcow2",
            "--images-dir",
            str(imported.parent),
        ],
    )

    assert result.exit_code == 0
    db = MVMDatabase()
    db.migrate()
    images = db.list_images()
    assert len(images) > 0
    img = images[0]
    assert img.fs_uuid is not None
    assert len(img.fs_uuid) > 0


# ---------------------------------------------------------------------------
# image rm
# ---------------------------------------------------------------------------


def _write_image_meta(cache_dir: Path, full_hash: str, filename: str, **extra: object) -> None:
    """Seed an image entry in the SQLite database."""
    db = MVMDatabase()
    db.migrate()
    db.upsert_image(
        Image(
            id=full_hash,
            os_slug=str(extra.get("internal_id")) if extra.get("internal_id") else full_hash,
            path=filename,
            os_name=extra.get("os_name"),
            fs_type=extra.get("fs_type", "ext4"),
            fs_uuid=extra.get("fs_uuid"),
            compressed_size=extra.get("compressed_size"),
            original_size=extra.get("original_size"),
            compression_ratio=extra.get("compression_ratio"),
            compressed_format=extra.get("compressed_format"),
            pulled_at=extra.get("pulled_at", "2026-01-01T00:00:00+00:00"),
            is_default=bool(extra.get("is_default", False)),
            created_at=extra.get("created_at"),
            updated_at=extra.get("updated_at", "2026-01-01T00:00:00+00:00"),
        )
    )


def _seed_binary_db(
    cache_dir: Path,
    name: str = "firecracker",
    version: str = "1.15.0",
    full_version: str = "v1.15.0",
    ci_version: str = "v1.15",
    binary_path: str | None = None,
    is_default: bool = True,
) -> None:
    """Seed a binary entry in the SQLite database."""
    db = MVMDatabase()
    db.migrate()
    db.upsert_binary(
        Binary(
            id=hashlib.sha256(f"{name}:{version}".encode()).hexdigest(),
            name=name,
            version=version,
            path=binary_path or str(cache_dir / "bin" / f"firecracker-{full_version}"),
            full_version=full_version,
            ci_version=ci_version,
            is_default=is_default,
        )
    )


def test_image_rm_success(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    full_hash = "a" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(cache_dir, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not img_file.exists()


def test_image_rm_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    result = click_runner.invoke(
        main_app,
        ["image", "rm", "abcdef", "--images-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


def test_image_rm_proceeds_without_confirmation(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    """Test that image rm proceeds without confirmation prompt."""
    full_hash = "c" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(cache_dir, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 0
    assert not img_file.exists()


def test_image_rm_multiple_ids(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    """Test that image rm removes multiple images without confirmation."""
    (tmp_path / "images").mkdir(exist_ok=True)
    hashes = ["d" * 64, "e" * 64]
    for h in hashes:
        img_file = tmp_path / "images" / f"{h}.ext4"
        img_file.write_text("fake")
        _write_image_meta(cache_dir, h, img_file.name)
    result = click_runner.invoke(
        main_app,
        [
            "image",
            "rm",
            hashes[0][:6],
            hashes[1][:6],
            "--images-dir",
            str(tmp_path / "images"),
        ],
    )
    assert result.exit_code == 0
    for h in hashes:
        assert not (tmp_path / "images" / f"{h}.ext4").exists()


# ---------------------------------------------------------------------------
# bin ls
# ---------------------------------------------------------------------------


def test_bin_ls_with_local():
    fake_versions = [
        BinaryVersion(
            version="1.5.0",
            firecracker_path=Path("/cache/bin/firecracker-v1.5.0"),
            jailer_path=Path("/cache/bin/jailer-v1.5.0"),
            is_active=True,
        ),
    ]
    with patch("mvmctl.cli.bin.list_local_versions", return_value=fake_versions):
        result = click_runner.invoke(main_app, ["bin", "ls"])
    assert result.exit_code == 0
    assert "1.5.0" in result.output


def test_bin_ls_empty():
    with patch("mvmctl.cli.bin.list_local_versions", return_value=[]):
        result = click_runner.invoke(main_app, ["bin", "ls"])
    assert result.exit_code == 0
    assert "No local binaries" in result.output


def test_bin_ls_with_remote():
    local = [
        BinaryVersion(
            version="1.5.0",
            firecracker_path=Path("/cache/bin/firecracker-v1.5.0"),
            jailer_path=Path("/cache/bin/jailer-v1.5.0"),
            is_active=False,
        ),
    ]
    remote = ["1.6.0", "1.5.0", "1.4.0"]
    with (
        patch("mvmctl.cli.bin.list_local_versions", return_value=local),
        patch("mvmctl.cli.bin.list_remote_versions", return_value=remote),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 0
    assert "1.6.0" in result.output
    assert "1.5.0" in result.output


def test_bin_ls_remote_error():
    with (
        patch("mvmctl.cli.bin.list_local_versions", return_value=[]),
        patch("mvmctl.cli.bin.list_remote_versions", side_effect=BinaryError("network fail")),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 1


def test_bin_ls_with_limit():
    with (
        patch("mvmctl.cli.bin.list_local_versions", return_value=[]),
        patch("mvmctl.cli.bin.list_remote_versions", return_value=["1.6.0"]) as mock_remote,
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote", "--limit", "5"])
    assert result.exit_code == 0
    mock_remote.assert_called_once_with(limit=5)


# ---------------------------------------------------------------------------
# bin fetch
# ---------------------------------------------------------------------------


def test_bin_fetch_success():
    bv = BinaryVersion(
        version="1.5.0",
        firecracker_path=Path("/cache/bin/firecracker-v1.5.0"),
        jailer_path=Path("/cache/bin/jailer-v1.5.0"),
        is_active=False,
    )
    with patch("mvmctl.cli.bin.fetch_binary", return_value=bv):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 0
    assert "Downloaded" in result.output
    assert "1.5.0" in result.output


def test_bin_fetch_error():
    with patch("mvmctl.cli.bin.fetch_binary", side_effect=BinaryError("download failed")):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 1


def test_bin_fetch_auto_sets_default_when_none_exists():
    bv = BinaryVersion(
        version="1.5.0",
        firecracker_path=Path("/cache/bin/firecracker-v1.5.0"),
        jailer_path=Path("/cache/bin/jailer-v1.5.0"),
        is_active=True,
    )
    with patch("mvmctl.cli.bin.fetch_binary", return_value=bv):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 0
    assert "Downloaded" in result.output
    assert "Default binary set to v1.5.0" in result.output


def test_bin_fetch_no_default_change_when_default_exists():
    bv = BinaryVersion(
        version="1.5.0",
        firecracker_path=Path("/cache/bin/firecracker-v1.5.0"),
        jailer_path=Path("/cache/bin/jailer-v1.5.0"),
        is_active=False,
    )
    with patch("mvmctl.cli.bin.fetch_binary", return_value=bv):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 0
    assert "Downloaded" in result.output
    assert "Default binary set" not in result.output


# ---------------------------------------------------------------------------
# bin set-default
# ---------------------------------------------------------------------------


def test_bin_set_default_success():
    with patch("mvmctl.cli.bin.set_active_version") as mock_set:
        result = click_runner.invoke(main_app, ["bin", "set-default", "1.5.0"])
    assert result.exit_code == 0
    assert "Active version set" in result.output
    mock_set.assert_called_once_with("1.5.0")


def test_bin_set_default_not_found():
    with patch(
        "mvmctl.cli.bin.set_active_version",
        side_effect=AssetNotFoundError("not downloaded"),
    ):
        result = click_runner.invoke(main_app, ["bin", "set-default", "9.9.9"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# bin rm
# ---------------------------------------------------------------------------


def test_bin_rm_success():
    with patch("mvmctl.cli.bin.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"])
    assert result.exit_code == 0
    assert "Removed" in result.output
    mock_rm.assert_called_once_with("1.5.0")


def test_bin_rm_not_found():
    with patch(
        "mvmctl.cli.bin.remove_version",
        side_effect=AssetNotFoundError("not found"),
    ):
        result = click_runner.invoke(main_app, ["bin", "rm", "9.9.9"])
    assert result.exit_code == 1


def test_bin_rm_proceeds_without_confirmation():
    """Test that bin rm proceeds without confirmation prompt."""
    with patch("mvmctl.cli.bin.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"])
    assert result.exit_code == 0
    mock_rm.assert_called_once_with("1.5.0")


def test_image_set_default(tmp_path: Path, monkeypatch):
    cache_dir = tmp_path / "cache"
    full_hash = "f" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)
    _write_image_meta(cache_dir, full_hash, img_file.name, os_name="Ubuntu 24.04")
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", full_hash[:6], "--images-dir", str(images_dir)],
    )
    assert result.exit_code == 0
    assert full_hash[:6] in result.output


def test_image_set_default_not_found(tmp_path: Path, monkeypatch):
    (tmp_path / "images").mkdir()
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", "abcdef", "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 1


def test_image_ls_remote(tmp_path: Path):
    with (
        patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.bin.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(tmp_path)]
        )
    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output


def test_kernel_set_default_cli(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    full_hash = "9" * 64
    vmlinux = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    vmlinux.write_bytes(b"\x7fELF")
    _write_kernel_meta(cache_dir, full_hash, vmlinux.name)
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert vmlinux.name in result.output


def test_kernel_set_default_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", "abcdef", "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


def test_bin_ls_default_limit():
    from mvmctl.cli.bin import bin_app

    result = runner.invoke(bin_app, ["ls", "--help"])
    assert "5" in result.output


def test_kernel_ls_auto_creates_dir(tmp_path: Path):
    missing = tmp_path / "kernels"
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(missing)])
    assert result.exit_code == 0
    assert missing.exists()


@patch("mvmctl.cli.bin.fetch_image")
@patch("mvmctl.cli.bin.load_images_config")
def test_image_fetch_confirms_existing_image(mock_config, mock_fetch, tmp_path):
    """FIX-009: image fetch warns when image already exists."""
    from click.testing import CliRunner as _ClickRunner

    from mvmctl.main import app
    from mvmctl.models.image import ImageSpec

    mock_config.return_value = [
        ImageSpec(
            id="ubuntu-24.04",
            image_type="ubuntu",
            version="24.04",
            name="Ubuntu 24.04 LTS",
            source="https://example.com/ubuntu.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=2048,
            sha256="abc" * 21 + "a",
        )
    ]
    # Pre-create existing COMPRESSED image file (the final expected format)
    (tmp_path / "ubuntu-24.04.ext4.zst").touch()
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4.zst", fs_type="ext4", fs_uuid="test-uuid"
    )

    # User says NO to re-download
    result = _ClickRunner().invoke(
        app,
        ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)],
        input="n\n",  # Answer 'no' to confirm prompt
    )
    assert result.exit_code == 0
    mock_fetch.assert_not_called()  # Should not have called fetch since compressed exists


def test_bin_rm_multiple_versions():
    with patch("mvmctl.cli.bin.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0", "1.6.0"])
    assert result.exit_code == 0
    assert mock_rm.call_count == 2


def test_bin_rm_no_args():
    result = click_runner.invoke(main_app, ["bin", "rm"])
    assert result.exit_code == 1


def test_kernel_rm_multiple(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    hash_a, hash_b = "d" * 64, "e" * 64
    (tmp_path / "vmlinux-official-6.19.9-x86_64").write_bytes(b"\x7fELF")
    (tmp_path / "vmlinux-fc-6.1.102-x86_64").write_bytes(b"\x7fELF")
    _write_kernel_meta(cache_dir, hash_a, "vmlinux-official-6.19.9-x86_64", version="6.19.9")
    _write_kernel_meta(cache_dir, hash_b, "vmlinux-fc-6.1.102-x86_64", version="6.1.102")
    result = runner.invoke(
        kernel_app,
        ["rm", hash_a[:6], hash_b[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert not (tmp_path / "vmlinux-official-6.19.9-x86_64").exists()
    assert not (tmp_path / "vmlinux-fc-6.1.102-x86_64").exists()


def test_kernel_rm_no_args(tmp_path: Path):
    result = runner.invoke(kernel_app, ["rm", "--kernels-dir", str(tmp_path)])
    assert result.exit_code == 1


def test_kernel_rm_blocked_when_referenced_by_vm(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that kernel rm is blocked when kernel is referenced by a VM."""
    cache_dir = tmp_path / "cache"

    full_hash = "a" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(cache_dir, full_hash, kernel.name)

    # Mock VM manager to return a VM using this kernel
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config.kernel_path = kernel
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1
    assert "referenced by" in result.output.lower() or "active vms" in result.output.lower()
    assert kernel.exists()  # Kernel should NOT be removed


def test_kernel_rm_with_force_removes_referenced(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that kernel rm with --force removes kernel even when referenced."""
    cache_dir = tmp_path / "cache"

    full_hash = "b" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(cache_dir, full_hash, kernel.name)

    # Mock VM manager to return a VM using this kernel
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config.kernel_path = kernel
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not kernel.exists()  # Kernel should be removed with --force


def test_image_rm_no_args(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    result = click_runner.invoke(main_app, ["image", "rm", "--images-dir", str(tmp_path)])
    assert result.exit_code == 1


def test_image_rm_blocked_when_referenced_by_vm(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that image rm is blocked when image is referenced by a VM."""
    cache_dir = tmp_path / "cache"

    full_hash = "a" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(cache_dir, full_hash, img_file.name)

    # Mock VM manager to return a VM using this image
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config.rootfs_path = img_file
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 1
    assert "referenced by" in result.output.lower() or "active vms" in result.output.lower()
    assert img_file.exists()  # Image should NOT be removed


def test_image_rm_with_force_removes_referenced(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that image rm with --force removes image even when referenced."""
    cache_dir = tmp_path / "cache"

    full_hash = "b" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(cache_dir, full_hash, img_file.name)

    # Mock VM manager to return a VM using this image
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config.rootfs_path = img_file
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images"), "--force"],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not img_file.exists()  # Image should be removed with --force


def test_image_rm_ambiguous(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"

    (tmp_path / "images").mkdir(exist_ok=True)
    for i, suffix in enumerate(["aaa", "aab"]):
        fh = "f" * 61 + suffix
        _write_image_meta(cache_dir, fh, f"{fh}.ext4", os_name=chr(65 + i))
        (tmp_path / "images" / f"{fh}.ext4").write_text("fake")
    result = click_runner.invoke(
        main_app, ["image", "rm", "f", "--images-dir", str(tmp_path / "images")]
    )
    assert result.exit_code == 1
    assert (
        "Ambiguous" in result.output
        or "ambiguous" in result.output.lower()
        or "matches" in result.output
    )


def test_image_rm_missing_file(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    full_hash = "9" * 64
    _write_image_meta(cache_dir, full_hash, f"{full_hash}.ext4")
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path)],
    )
    assert result.exit_code == 1
    assert "not found" in result.output.lower() or "missing" in result.output.lower()


def test_image_ls_with_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    full_hash = "1" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 64)
    _write_image_meta(
        tmp_path,
        full_hash,
        "ubuntu-24.04.ext4",
        os_name="Ubuntu 24.04 LTS",
        internal_id="ubuntu-24.04",
    )
    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--images-dir", str(tmp_path / "images")]
        )
    assert result.exit_code == 0
    assert full_hash[:6] in result.output
    assert "Ubuntu 24.04 LTS" in result.output


# ---------------------------------------------------------------------------
# State Validation X marks (Phase 4)
# ---------------------------------------------------------------------------


def test_kernel_ls_shows_x_mark_for_missing_file(tmp_path: Path, mocker):
    """Verify X prefix shown when kernel file missing."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create metadata entry for kernel but NOT the actual file
    full_hash = "a" * 64
    _write_kernel_meta(
        cache_dir, full_hash, "vmlinux-fc-6.1.9-x86_64", version="6.1.9", arch="x86_64"
    )

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "X " prefix in output (X mark for missing file)
    assert "X " in result.output


def test_kernel_ls_no_x_mark_for_existing_file(tmp_path: Path, mocker):
    """Verify no X prefix when kernel file exists."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create metadata entry AND kernel file
    full_hash = "b" * 64
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    _write_kernel_meta(
        cache_dir, full_hash, "vmlinux-fc-6.1.9-x86_64", version="6.1.9", arch="x86_64"
    )

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify no "X " prefix when file exists
    # The output should show normal spacing, not X mark
    lines = result.output.split("\n")
    for line in lines:
        if "vmlinux-fc" in line:
            # Should not have X prefix for existing file
            assert not line.startswith("X ")


def test_image_ls_shows_x_mark_for_missing_file(tmp_path: Path, mocker):
    """Verify X prefix shown when image file missing."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create metadata entry but NOT the actual file
    full_hash = "c" * 64
    _write_image_meta(cache_dir, full_hash, "ubuntu-24.04.ext4", os_name="Ubuntu 24.04")

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_image_ls_no_x_mark_for_existing_file(tmp_path: Path, mocker):
    """Verify no X prefix when image file exists."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create metadata entry AND image file
    full_hash = "d" * 64
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(cache_dir, full_hash, "ubuntu-24.04.ext4", os_name="Ubuntu 24.04")

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify no X mark for existing file
    lines = result.output.split("\n")
    for line in lines:
        if "ubuntu-24.04" in line.lower():
            assert not line.startswith("X ")


def test_bin_ls_shows_x_mark_for_missing_binary(tmp_path: Path, mocker):
    """Verify X prefix shown when binary file missing."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    _seed_binary_db(cache_dir, binary_path=str(bin_dir / "firecracker-v1.15.0"))

    result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_bin_ls_no_x_mark_for_existing_binary(tmp_path: Path, mocker):
    """Verify no X prefix when binary file exists."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    # Create metadata entry AND binary file
    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")

    _seed_binary_db(cache_dir, binary_path=str(fc_bin))

    result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify no X mark for existing file
    lines = result.output.split("\n")
    for line in lines:
        if "1.15.0" in line:
            assert not line.startswith("X ")


# ---------------------------------------------------------------------------
# Size column tests (Phase 4)
# ---------------------------------------------------------------------------


def test_kernel_ls_shows_size_column(tmp_path: Path, mocker):
    """Verify kernel ls displays size column."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel file with known size (~10MB)
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * (10 * 1024 * 1024))

    _write_kernel_meta(
        cache_dir, "a" * 64, "vmlinux-fc-6.1.9-x86_64", version="6.1.9", arch="x86_64"
    )

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size column shows formatted size (e.g., "10.0 MiB")
    assert "MiB" in result.output or "GiB" in result.output or "B" in result.output


def test_kernel_ls_size_format_bytes(tmp_path: Path, mocker):
    """Verify size formatting for small files (bytes)."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create small kernel file (< 1 MiB)
    kernel_file = kernels_dir / "vmlinux-small"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 512)  # 516 bytes

    _write_kernel_meta(cache_dir, "b" * 64, "vmlinux-small", version="6.1.9", arch="x86_64")

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in bytes or appropriate unit
    assert "B" in result.output


def test_kernel_ls_size_format_mib(tmp_path: Path, mocker):
    """Verify size formatting for medium files (MiB)."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel file (1-1024 MiB)
    kernel_file = kernels_dir / "vmlinux-medium"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * (100 * 1024 * 1024))  # ~100 MiB

    _write_kernel_meta(cache_dir, "c" * 64, "vmlinux-medium", version="6.1.9", arch="x86_64")

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in MiB
    assert "MiB" in result.output


def test_kernel_ls_size_format_gib(tmp_path: Path, mocker):
    """Verify size formatting for large files (GiB)."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create large kernel file (> 1024 MiB = 1 GiB)
    kernel_file = kernels_dir / "vmlinux-large"
    # Write in chunks to avoid memory issues
    with open(kernel_file, "wb") as f:
        f.write(b"\x7fELF")
        chunk = b"\x00" * (1024 * 1024)  # 1 MiB chunk
        for _ in range(1025):  # Just over 1 GiB
            f.write(chunk)

    _write_kernel_meta(cache_dir, "d" * 64, "vmlinux-large", version="6.1.9", arch="x86_64")

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in GiB
    assert "GiB" in result.output


def test_image_ls_shows_size_column(tmp_path: Path, mocker):
    """Verify image ls displays size column."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create image file with known size
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * (2 * 1024 * 1024 * 1024))  # 2 GiB

    _write_image_meta(cache_dir, "e" * 64, "ubuntu-24.04.ext4", os_name="Ubuntu 24.04")

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify size column shows formatted size
    assert "GiB" in result.output or "MiB" in result.output


def test_image_ls_size_various_units(tmp_path: Path, mocker):
    """Verify image ls shows sizes in various units."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create multiple images with different sizes
    # Small image (< 1 MiB)
    small_img = images_dir / "small.ext4"
    small_img.write_bytes(b"\x00" * 512)

    # Medium image (1-1024 MiB)
    medium_img = images_dir / "medium.ext4"
    medium_img.write_bytes(b"\x00" * (100 * 1024 * 1024))

    _write_image_meta(cache_dir, "f" * 64, "small.ext4", os_name="Small Image")
    _write_image_meta(cache_dir, "g" * 64, "medium.ext4", os_name="Medium Image")

    fake_images = [
        ImageSpec(
            id="small",
            image_type="test",
            version="1.0",
            name="Small Image",
            source="https://example.com/small.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=1,
            sha256=None,
        ),
        ImageSpec(
            id="medium",
            image_type="test",
            version="1.0",
            name="Medium Image",
            source="https://example.com/medium.qcow2",
            format="qcow2",
            convert_to="ext4",
            minimum_rootfs_size=100,
            sha256=None,
        ),
    ]

    with patch("mvmctl.cli.bin.load_images_config", return_value=fake_images):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify size units are shown
    assert "B" in result.output or "MiB" in result.output or "GiB" in result.output


# ---------------------------------------------------------------------------
# Default prefix tests (Phase 4)
# ---------------------------------------------------------------------------


def test_kernel_ls_shows_default_prefix(tmp_path: Path, mocker):
    """Verify * prefix shown for default kernel."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel with is_default=true
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    _write_kernel_meta(
        cache_dir,
        "h" * 64,
        "vmlinux-fc-6.1.9-x86_64",
        version="6.1.9",
        arch="x86_64",
        is_default=True,
    )

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "* " prefix in output for default kernel
    assert "* " in result.output
    # Verify NO "Def" column in output
    assert "Def" not in result.output


def test_kernel_ls_no_prefix_for_non_default(tmp_path: Path, mocker):
    """Verify no * prefix for non-default kernel."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel with is_default=false
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    _write_kernel_meta(
        cache_dir,
        "i" * 64,
        "vmlinux-fc-6.1.9-x86_64",
        version="6.1.9",
        arch="x86_64",
        is_default=False,
    )

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify no "* " prefix for non-default kernel
    # The line should start with "  " (two spaces) instead
    lines = result.output.split("\n")
    for line in lines:
        if "vmlinux-fc" in line and "Name" not in line:
            assert not line.startswith("* ")


def test_image_ls_shows_default_prefix(tmp_path: Path, mocker):
    """Verify * prefix shown for default image."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create image with is_default=true
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        cache_dir,
        "j" * 64,
        "ubuntu-24.04.ext4",
        os_name="Ubuntu 24.04",
        is_default=True,
    )

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "* " prefix in output
    assert "* " in result.output


def test_bin_ls_shows_default_prefix(tmp_path: Path, mocker):
    """Verify * prefix shown for default binary."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    # bin ls uses get_bin_dir() = cache_dir / "bin"
    bin_dir = cache_dir / "bin"
    bin_dir.mkdir(exist_ok=True)

    # Create binary with is_default=true
    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")
    jl_bin = bin_dir / "jailer-v1.15.0"
    jl_bin.write_bytes(b"fake jailer")
    (bin_dir / "firecracker").symlink_to(fc_bin.name)

    _seed_binary_db(cache_dir, binary_path=str(fc_bin))
    _seed_binary_db(cache_dir, name="jailer", binary_path=str(jl_bin), is_default=True)

    result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "* " prefix in output
    assert "* " in result.output


def test_kernel_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from kernel ls."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    _write_kernel_meta(cache_dir, "k" * 64, "vmlinux", version="6.1.9", arch="x86_64")

    result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


def test_image_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from image ls."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(cache_dir, "l" * 64, "ubuntu-24.04.ext4", os_name="Ubuntu 24.04")

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


def test_bin_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from bin ls."""
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")

    _seed_binary_db(cache_dir, binary_path=str(fc_bin))

    result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


# ---------------------------------------------------------------------------
# Asset removal protection tests with kernel_id/image_id (Issue 1 fix)
# ---------------------------------------------------------------------------


def test_kernel_rm_blocked_when_referenced_by_kernel_id(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that kernel rm is blocked when VM has kernel_id set but no config."""
    cache_dir = tmp_path / "cache"

    full_hash = "c" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(cache_dir, full_hash, kernel.name)

    # Mock VM manager to return a VM using kernel_id (no config)
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config = None  # No config persisted
    mock_vm.kernel_id = str(kernel)  # But kernel_id is set
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1
    assert "referenced by" in result.output.lower() or "active vms" in result.output.lower()
    assert kernel.exists()  # Kernel should NOT be removed


def test_image_rm_blocked_when_referenced_by_image_id(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that image rm is blocked when VM has image_id set but no config."""
    cache_dir = tmp_path / "cache"

    full_hash = "d" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(cache_dir, full_hash, img_file.name)

    # Mock VM manager to return a VM using image_id (no config)
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config = None  # No config persisted
    mock_vm.image_id = str(img_file)  # But image_id is set
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 1
    assert "referenced by" in result.output.lower() or "active vms" in result.output.lower()
    assert img_file.exists()  # Image should NOT be removed


def test_kernel_rm_with_kernel_id_and_force(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, mocker
):
    """Test that kernel rm with --force removes kernel even when referenced by kernel_id."""
    cache_dir = tmp_path / "cache"

    full_hash = "e" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(cache_dir, full_hash, kernel.name)

    # Mock VM manager to return a VM using kernel_id (no config)
    mock_vm = mocker.MagicMock()
    mock_vm.name = "test-vm"
    mock_vm.config = None
    mock_vm.kernel_id = str(kernel)
    mock_manager = mocker.MagicMock()
    mock_manager.list_all.return_value = [mock_vm]
    mocker.patch("mvmctl.cli.bin.get_vm_manager", return_value=mock_manager)

    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not kernel.exists()  # Kernel should be removed with --force


# ---------------------------------------------------------------------------
# image inspect tests
# ---------------------------------------------------------------------------


def test_image_inspect_success(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect shows image details."""
    full_hash = "a" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * (10 * 1024 * 1024))  # 10 MiB

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Ubuntu 24.04 LTS",
        internal_id="ubuntu-24.04",
        fs_type="ext4",
        fs_uuid="test-uuid-1234",
        compressed_format="zst",
        original_size=20 * 1024 * 1024,
        compressed_size=10 * 1024 * 1024,
        compression_ratio=2.0,
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "Ubuntu 24.04 LTS" in result.output
    assert "ubuntu-24.04" in result.output
    assert "ext4" in result.output
    assert "zst" in result.output


def test_image_inspect_json_output(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect --json outputs valid JSON."""
    full_hash = "b" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Test Image",
        internal_id="test-image",
        fs_type="ext4",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--json", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data["id"] == full_hash
    assert data["name"] == "Test Image"
    assert data["internal_id"] == "test-image"


def test_image_inspect_tree_output(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect --tree shows tree format."""
    full_hash = "c" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Tree Test",
        internal_id="tree-test",
        fs_type="ext4",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--tree", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "├──" in result.output or "└──" in result.output
    assert "tree-test" in result.output


def test_image_inspect_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect with non-existent ID."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    result = click_runner.invoke(
        main_app, ["image", "inspect", "nonexistent", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 1
    assert "No image found" in result.output or "not found" in result.output.lower()


def test_image_inspect_ambiguous_prefix(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    cache_dir = tmp_path / "cache"
    """Test image inspect with ambiguous ID prefix."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create two images with same prefix
    for i, suffix in enumerate(["aaa", "aab"]):
        full_hash = "a" * 61 + suffix
        img_file = images_dir / f"{full_hash}.ext4"
        img_file.write_bytes(b"\x00" * 1024)
        _write_image_meta(cache_dir, full_hash, img_file.name, os_name=f"Image {i}")

    result = click_runner.invoke(
        main_app, ["image", "inspect", "a", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 1
    assert "Ambiguous" in result.output or "ambiguous" in result.output.lower()


def test_image_inspect_missing_file(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect shows missing marker when file is gone."""
    full_hash = "d" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    # Note: NOT creating the actual file

    _write_image_meta(
        tmp_path,
        full_hash,
        f"{full_hash}.ext4",
        os_name="Missing Image",
        internal_id="missing-image",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "missing" in result.output.lower() or "(missing)" in result.output


# ---------------------------------------------------------------------------
# image ls --remote compression column tests
# ---------------------------------------------------------------------------


def test_image_ls_remote_shows_compression_column(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image ls --remote shows Compression column instead of Downloaded."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create downloaded image with compression metadata
    full_hash = "f" * 64
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * (100 * 1024 * 1024))  # 100 MiB

    _write_image_meta(
        tmp_path,
        full_hash,
        "ubuntu-24.04.ext4",
        os_name="Ubuntu 24.04 LTS",
        internal_id="ubuntu-24.04",
        compressed_format="zst",
        original_size=200 * 1024 * 1024,
        compressed_size=100 * 1024 * 1024,
        compression_ratio=2.0,
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(images_dir)]
        )

    assert result.exit_code == 0
    # Verify Compression column header is present
    assert "Compression" in result.output
    # Verify Downloaded column is NOT present
    assert "Downloaded" not in result.output
    # Verify compression format is shown for downloaded image
    assert "zst" in result.output


def test_image_ls_remote_shows_dash_for_not_downloaded(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    """Test image ls --remote shows '-' for images not downloaded."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # No images downloaded, no metadata - empty DB is fine

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(images_dir)]
        )

    assert result.exit_code == 0
    # Verify Compression column header is present
    assert "Compression" in result.output


def test_image_ls_remote_missing_file_shows_x_marker(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    """Test image ls --remote shows X marker for missing files."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create metadata but NOT the actual file
    full_hash = "g" * 64
    _write_image_meta(
        tmp_path,
        full_hash,
        "ubuntu-24.04.ext4",
        os_name="Ubuntu 24.04 LTS",
        internal_id="ubuntu-24.04",
        compressed_format="zst",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(images_dir)]
        )

    assert result.exit_code == 0
    # Verify X marker is shown for missing file
    assert "X " in result.output


def test_image_inspect_with_compression_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect shows compression details."""
    full_hash = "h" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * (50 * 1024 * 1024))

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Compressed Image",
        internal_id="compressed-img",
        fs_type="ext4",
        fs_uuid="uuid-1234",
        original_size=100 * 1024 * 1024,
        compressed_size=50 * 1024 * 1024,
        compression_ratio=2.0,
        compressed_format="zst",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "COMPRESSION" in result.output
    assert "zst" in result.output
    assert "2.00x" in result.output
    assert "MiB" in result.output


def test_image_inspect_with_internal_id(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect shows internal_id when available."""
    full_hash = "i" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Ubuntu 24.04",
        internal_id="ubuntu-24.04",
        fs_type="ext4",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output
    assert "Internal ID" in result.output


def test_image_ls_remote_with_compression_in_metadata(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    """Test image ls --remote shows compression from metadata for downloaded images."""
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * (100 * 1024 * 1024))

    full_hash = "j" * 64
    _write_image_meta(
        tmp_path,
        full_hash,
        "ubuntu-24.04.ext4",
        os_name="Ubuntu 24.04 LTS",
        internal_id="ubuntu-24.04",
        compressed_format="zst",
        original_size=200 * 1024 * 1024,
        compressed_size=100 * 1024 * 1024,
        compression_ratio=2.0,
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    with patch("mvmctl.cli.bin.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(images_dir)]
        )

    assert result.exit_code == 0
    assert "Compression" in result.output
    assert "zst" in result.output


def test_image_inspect_with_filename_lookup(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect finds image by filename."""
    full_hash = "k" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / "custom-image.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        "custom-image.ext4",
        os_name="Custom Image",
        internal_id="custom",
        fs_type="ext4",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "Custom Image" in result.output
    assert "custom-image.ext4" in result.output


def test_image_inspect_tree_format(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect --tree shows tree format with all sections."""
    full_hash = "l" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="Tree Test Image",
        internal_id="tree-test",
        fs_type="ext4",
        fs_uuid="test-uuid",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--tree", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    assert "├──" in result.output
    assert "└──" in result.output
    assert "tree-test" in result.output


def test_image_inspect_json_format(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test image inspect --json outputs valid JSON with all fields."""
    full_hash = "m" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / f"{full_hash}.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    _write_image_meta(
        tmp_path,
        full_hash,
        img_file.name,
        os_name="JSON Test",
        internal_id="json-test",
        fs_type="ext4",
        fs_uuid="json-uuid",
        pulled_at="2026-01-15T10:30:00+00:00",
    )

    result = click_runner.invoke(
        main_app, ["image", "inspect", full_hash[:6], "--json", "--images-dir", str(images_dir)]
    )

    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data["id"] == full_hash
    assert data["name"] == "JSON Test"
    assert data["internal_id"] == "json-test"
    assert data["fs_type"] == "ext4"
    assert data["fs_uuid"] == "json-uuid"
