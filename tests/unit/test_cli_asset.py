"""Tests for CLI asset commands (kernel, image, bin, cache clear)."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from click.testing import CliRunner as ClickCliRunner
from typer.testing import CliRunner

from mvmctl.cli.asset import kernel_app
from mvmctl.core.binary_manager import BinaryVersion
from mvmctl.core.image import ImageImportResult
from mvmctl.exceptions import AssetNotFoundError, BinaryError, KernelError
from mvmctl.main import app as main_app
from mvmctl.models.image import ImageSpec
from mvmctl.models.kernel import KernelSpec

runner = CliRunner()
click_runner = ClickCliRunner()

_FAKE_IMAGES = [
    ImageSpec(
        id="ubuntu-24.04",
        image_type="ubuntu",
        version="24.04",
        name="Ubuntu 24.04 LTS",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
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
        size_mib=2048,
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
    import json as _json

    (cache_dir / "metadata.json").write_text(
        _json.dumps(
            {"kernels": {"vmlinux": {"last_modified": "2026-01-01T00:00:00"}}, "images": {}}
        )
    )
    import os

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path)])
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
    (cache_dir / "metadata.json").write_text(
        json.dumps({"kernels": {"vmlinux": {"last_modified": "2026-01-01T00:00:00"}}, "images": {}})
    )
    import os

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
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
    (cache_dir / "metadata.json").write_text(
        json.dumps(
            {
                "kernels": {
                    "vmlinux": {"last_modified": "2026-01-01T00:00:00"},
                    "vmlinux-6.1.102": {"last_modified": "2026-01-02T00:00:00"},
                },
                "images": {},
            }
        )
    )
    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
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
    (cache_dir / "metadata.json").write_text(
        json.dumps({"kernels": {"vmlinux": {"last_modified": "2026-01-01T00:00:00"}}, "images": {}})
    )
    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = [e["name"] for e in data]
    assert "vmlinux" in names
    assert "somefile.txt" not in names


# ---------------------------------------------------------------------------
# kernel fetch
# ---------------------------------------------------------------------------


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline", side_effect=KernelError("build failed"))
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.download_firecracker_kernel")
@patch("mvmctl.cli.asset._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.download_firecracker_kernel")
@patch("mvmctl.cli.asset._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.download_firecracker_kernel")
@patch("mvmctl.cli.asset._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.download_firecracker_kernel")
@patch("mvmctl.cli.asset._get_ci_version", return_value="1.12")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


@patch("mvmctl.cli.asset.resolve_kernel_spec", side_effect=KernelError("ambiguous type"))
def test_kernel_fetch_type_ambiguity_error(mock_resolve: MagicMock):
    result = click_runner.invoke(main_app, ["kernel", "fetch", "--type", "firecracker"])
    assert result.exit_code == 1
    assert "ambiguous type" in result.output


@patch("mvmctl.cli.asset.build_kernel_pipeline")
@patch("mvmctl.cli.asset.resolve_kernel_spec")
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


def _write_kernel_meta(
    cache_dir: Path, full_hash: str, filename: str, version: str = "6.1.9"
) -> None:
    import json

    meta_file = cache_dir / "metadata.json"
    data: dict = {}
    if meta_file.exists():
        data = json.loads(meta_file.read_text())
    data.setdefault("kernels", {})[full_hash] = {
        "filename": filename,
        "name": filename,
        "full_hash": full_hash,
        "version": version,
        "type": "firecracker",
        "arch": "x86_64",
        "last_modified": "2026-01-01T12:00:00+00:00",
    }
    meta_file.write_text(json.dumps(data, indent=2))


def test_kernel_rm_success(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "a" * 64
    kernel = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    kernel.write_bytes(b"\x7fELF" + b"\x00" * 1024)
    _write_kernel_meta(tmp_path, full_hash, kernel.name)
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not kernel.exists()


def test_kernel_rm_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))
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
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "Ubuntu 24.04 LTS" in result.output
    assert "Debian 12" in result.output


def test_image_ls_json():
    with patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    names = {item["name"] for item in data}
    assert "Ubuntu 24.04 LTS" in names or len(data) == 0


def test_image_ls_empty():
    with patch("mvmctl.cli.asset.load_images_config", return_value=[]):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data == []


def test_image_ls_shows_cached_marker(tmp_path: Path):
    (tmp_path / "ubuntu-24.04.ext4").touch()
    with (
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0


# ---------------------------------------------------------------------------
# image fetch
# ---------------------------------------------------------------------------


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.asset._save_image_meta")
@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
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


@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_not_found(mock_config: MagicMock):
    result = click_runner.invoke(main_app, ["image", "fetch", "nonexistent"])
    assert result.exit_code == 1


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.asset._save_image_meta")
@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config")
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
            size_mib=2048,
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
            size_mib=2048,
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


@patch("mvmctl.cli.asset.load_images_config")
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
            size_mib=2048,
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
            size_mib=2048,
            sha256=None,
        ),
    ]

    result = click_runner.invoke(main_app, ["image", "fetch", "ubuntu"])
    assert result.exit_code == 1
    assert "Provide --version" in result.output


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.asset._save_image_meta")
@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config")
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
            size_mib=2048,
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


@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_type_option_conflicts_with_id(mock_config: MagicMock):
    result = click_runner.invoke(
        main_app,
        ["image", "fetch", "ubuntu-24.04", "--type", "ubuntu"],
    )
    assert result.exit_code == 1
    assert "cannot be used when selector is an image ID" in result.output


@patch("mvmctl.cli.asset.fetch_image", return_value=None)
@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_failure(mock_config: MagicMock, mock_fetch: MagicMock, tmp_path: Path):
    result = click_runner.invoke(
        main_app, ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)]
    )
    assert result.exit_code == 1


@patch("pathlib.Path.read_bytes", return_value=b"mocked")
@patch("mvmctl.cli.asset._save_image_meta")
@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
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


@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_saves_fs_uuid_in_metadata(
    mock_config: MagicMock,
    mock_fetch: MagicMock,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))
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
    data = json.loads((tmp_path / "cache" / "metadata.json").read_text())
    image_entries = data.get("images", {})
    assert len(image_entries) == 1
    entry = next(iter(image_entries.values()))
    assert entry.get("fs_uuid") == "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"


@patch("mvmctl.cli.asset.import_image")
def test_image_import_saves_fs_uuid_in_metadata(
    mock_import: MagicMock,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
):
    import json

    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path / "cache"))
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
    data = json.loads((tmp_path / "cache" / "metadata.json").read_text())
    image_entries = data.get("images", {})
    assert len(image_entries) == 1
    entry = next(iter(image_entries.values()))
    assert entry.get("fs_uuid") == "ffffffff-1111-2222-3333-444444444444"


# ---------------------------------------------------------------------------
# image rm
# ---------------------------------------------------------------------------


def _write_image_meta(
    cache_dir: Path, full_hash: str, filename: str, os_name: str = "Test"
) -> None:
    import json

    meta_file = cache_dir / "metadata.json"
    data: dict = {}
    if meta_file.exists():
        data = json.loads(meta_file.read_text())
    data.setdefault("images", {})[full_hash] = {
        "os_name": os_name,
        "filename": filename,
        "fs_type": filename.rsplit(".", 1)[-1],
        "pulled_at": "2026-01-01T12:00:00+00:00",
        "full_hash": full_hash,
    }
    meta_file.write_text(json.dumps(data, indent=2))


def test_image_rm_success(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "a" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(tmp_path, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not img_file.exists()


def test_image_rm_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    result = click_runner.invoke(
        main_app,
        ["image", "rm", "abcdef", "--images-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


def test_image_rm_proceeds_without_confirmation(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test that image rm proceeds without confirmation prompt."""
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "c" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(tmp_path, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 0
    assert not img_file.exists()


def test_image_rm_multiple_ids(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test that image rm removes multiple images without confirmation."""
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    (tmp_path / "images").mkdir(exist_ok=True)
    hashes = ["d" * 64, "e" * 64]
    for h in hashes:
        img_file = tmp_path / "images" / f"{h}.ext4"
        img_file.write_text("fake")
        _write_image_meta(tmp_path, h, img_file.name)
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
    with patch("mvmctl.cli.asset.list_local_versions", return_value=fake_versions):
        result = click_runner.invoke(main_app, ["bin", "ls"])
    assert result.exit_code == 0
    assert "1.5.0" in result.output


def test_bin_ls_empty():
    with patch("mvmctl.cli.asset.list_local_versions", return_value=[]):
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
        patch("mvmctl.cli.asset.list_local_versions", return_value=local),
        patch("mvmctl.cli.asset.list_remote_versions", return_value=remote),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 0
    assert "1.6.0" in result.output
    assert "1.5.0" in result.output


def test_bin_ls_remote_error():
    with (
        patch("mvmctl.cli.asset.list_local_versions", return_value=[]),
        patch("mvmctl.cli.asset.list_remote_versions", side_effect=BinaryError("network fail")),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 1


def test_bin_ls_with_limit():
    with (
        patch("mvmctl.cli.asset.list_local_versions", return_value=[]),
        patch("mvmctl.cli.asset.list_remote_versions", return_value=["1.6.0"]) as mock_remote,
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
    with patch("mvmctl.cli.asset.fetch_binary", return_value=bv):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 0
    assert "Downloaded" in result.output
    assert "1.5.0" in result.output


def test_bin_fetch_error():
    with patch("mvmctl.cli.asset.fetch_binary", side_effect=BinaryError("download failed")):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# bin set-default
# ---------------------------------------------------------------------------


def test_bin_set_default_success():
    with patch("mvmctl.cli.asset.set_active_version") as mock_set:
        result = click_runner.invoke(main_app, ["bin", "set-default", "1.5.0"])
    assert result.exit_code == 0
    assert "Active version set" in result.output
    mock_set.assert_called_once_with("1.5.0")


def test_bin_set_default_not_found():
    with patch(
        "mvmctl.cli.asset.set_active_version",
        side_effect=AssetNotFoundError("not downloaded"),
    ):
        result = click_runner.invoke(main_app, ["bin", "set-default", "9.9.9"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# bin rm
# ---------------------------------------------------------------------------


def test_bin_rm_success():
    with patch("mvmctl.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"])
    assert result.exit_code == 0
    assert "Removed" in result.output
    mock_rm.assert_called_once_with("1.5.0")


def test_bin_rm_not_found():
    with patch(
        "mvmctl.cli.asset.remove_version",
        side_effect=AssetNotFoundError("not found"),
    ):
        result = click_runner.invoke(main_app, ["bin", "rm", "9.9.9"])
    assert result.exit_code == 1


def test_bin_rm_proceeds_without_confirmation():
    """Test that bin rm proceeds without confirmation prompt."""
    with patch("mvmctl.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"])
    assert result.exit_code == 0
    mock_rm.assert_called_once_with("1.5.0")


# ---------------------------------------------------------------------------
# cache clear
# ---------------------------------------------------------------------------


def test_cache_clear_dirs_exist(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "kernels").mkdir()
    (tmp_path / "images").mkdir()
    (tmp_path / "bin" / "firecracker-v1.0.0").touch()

    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not (tmp_path / "bin").exists()
    assert not (tmp_path / "kernels").exists()
    assert not (tmp_path / "images").exists()


def test_cache_clear_nothing_to_clear(tmp_path: Path):
    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert "Nothing to clear" in result.output


def test_cache_clear_partial_dirs(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert not (tmp_path / "bin").exists()


def test_cache_clear_with_confirmation(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "kernels").mkdir()
    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear"], input="y\n")
    assert result.exit_code == 0
    assert not (tmp_path / "bin").exists()


def test_cache_clear_abort_confirmation(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear"], input="n\n")
    assert result.exit_code != 0
    assert (tmp_path / "bin").exists()


def test_cache_clear_preserves_vms_dir(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "vms").mkdir()
    (tmp_path / "vms" / "state.json").write_text("{}")
    with patch("mvmctl.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert (tmp_path / "vms").exists()


def test_image_set_default(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "f" * 64
    images_dir = tmp_path / "images"
    images_dir.mkdir()
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)
    _write_image_meta(tmp_path, full_hash, img_file.name, os_name="Ubuntu 24.04")
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", full_hash[:6], "--images-dir", str(images_dir)],
    )
    assert result.exit_code == 0
    assert full_hash[:6] in result.output


def test_image_set_default_not_found(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    (tmp_path / "images").mkdir()
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", "abcdef", "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 1


def test_image_ls_remote(tmp_path: Path):
    with (
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("mvmctl.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(tmp_path)]
        )
    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output


def test_kernel_set_default_cli(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "9" * 64
    vmlinux = tmp_path / "vmlinux-fc-6.1.9-x86_64"
    vmlinux.write_bytes(b"\x7fELF")
    _write_kernel_meta(tmp_path, full_hash, vmlinux.name)
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", full_hash[:6], "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert vmlinux.name in result.output


def test_kernel_set_default_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", "abcdef", "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


def test_bin_ls_default_limit():
    from mvmctl.cli.asset import bin_app

    result = runner.invoke(bin_app, ["ls", "--help"])
    assert "5" in result.output


def test_kernel_ls_auto_creates_dir(tmp_path: Path):
    missing = tmp_path / "kernels"
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(missing)])
    assert result.exit_code == 0
    assert missing.exists()


@patch("mvmctl.cli.asset.fetch_image")
@patch("mvmctl.cli.asset.load_images_config")
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
            size_mib=2048,
            sha256="abc" * 21 + "a",
        )
    ]
    # Pre-create existing image file
    (tmp_path / "ubuntu-24.04.ext4").touch()
    mock_fetch.return_value = ImageImportResult(
        path=tmp_path / "ubuntu-24.04.ext4", fs_type="ext4", fs_uuid="test-uuid"
    )

    # User says NO to re-download
    result = _ClickRunner().invoke(
        app,
        ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)],
        input="n\n",  # Answer 'no' to confirm prompt
    )
    assert result.exit_code == 0
    mock_fetch.assert_not_called()  # Should not have called fetch


def test_bin_rm_multiple_versions():
    with patch("mvmctl.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0", "1.6.0"])
    assert result.exit_code == 0
    assert mock_rm.call_count == 2


def test_bin_rm_no_args():
    result = click_runner.invoke(main_app, ["bin", "rm"])
    assert result.exit_code == 1


def test_kernel_rm_multiple(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    hash_a, hash_b = "d" * 64, "e" * 64
    (tmp_path / "vmlinux-official-6.19.9-x86_64").write_bytes(b"\x7fELF")
    (tmp_path / "vmlinux-fc-6.1.102-x86_64").write_bytes(b"\x7fELF")
    _write_kernel_meta(tmp_path, hash_a, "vmlinux-official-6.19.9-x86_64", version="6.19.9")
    _write_kernel_meta(tmp_path, hash_b, "vmlinux-fc-6.1.102-x86_64", version="6.1.102")
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


def test_image_rm_no_args(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    result = click_runner.invoke(main_app, ["image", "rm", "--images-dir", str(tmp_path)])
    assert result.exit_code == 1


def test_image_rm_ambiguous(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    import json

    (tmp_path / "images").mkdir(exist_ok=True)
    meta = {
        "images": {
            "f" * 64: {
                "os_name": "A",
                "filename": f"{'f' * 64}.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T00:00:00+00:00",
                "full_hash": "f" * 64,
            },
            "f" + "0" * 63: {
                "os_name": "B",
                "filename": f"{'f' + '0' * 63}.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T00:00:00+00:00",
                "full_hash": "f" + "0" * 63,
            },
        }
    }
    (tmp_path / "metadata.json").write_text(json.dumps(meta))
    for key in meta["images"]:
        (tmp_path / "images" / f"{key}.ext4").write_text("fake")
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
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "9" * 64
    _write_image_meta(tmp_path, full_hash, f"{full_hash}.ext4")
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path)],
    )
    assert result.exit_code == 1
    assert "not found" in result.output.lower() or "missing" in result.output.lower()


def test_image_ls_with_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    full_hash = "1" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 64)
    import json

    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04 LTS",
                "internal_id": "ubuntu-24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T00:00:00+00:00",
                "full_hash": full_hash,
            }
        }
    }
    (tmp_path / "metadata.json").write_text(json.dumps(meta))
    with patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES):
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
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create metadata entry for kernel but NOT the actual file
    full_hash = "a" * 64
    meta = {
        "kernels": {
            full_hash: {
                "filename": "vmlinux-fc-6.1.9-x86_64",
                "name": "vmlinux-fc-6.1.9-x86_64",
                "full_hash": full_hash,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "X " prefix in output (X mark for missing file)
    assert "X " in result.output


def test_kernel_ls_no_x_mark_for_existing_file(tmp_path: Path, mocker):
    """Verify no X prefix when kernel file exists."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create metadata entry AND kernel file
    full_hash = "b" * 64
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    meta = {
        "kernels": {
            full_hash: {
                "filename": "vmlinux-fc-6.1.9-x86_64",
                "name": "vmlinux-fc-6.1.9-x86_64",
                "full_hash": full_hash,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
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
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create metadata entry but NOT the actual file
    full_hash = "c" * 64
    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": full_hash,
            }
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_image_ls_no_x_mark_for_existing_file(tmp_path: Path, mocker):
    """Verify no X prefix when image file exists."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create metadata entry AND image file
    full_hash = "d" * 64
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": full_hash,
            }
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify no X mark for existing file
    lines = result.output.split("\n")
    for line in lines:
        if "ubuntu-24.04" in line.lower():
            assert not line.startswith("X ")


def test_bin_ls_shows_x_mark_for_missing_binary(tmp_path: Path, mocker):
    """Verify X prefix shown when binary file missing."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    # Create metadata entry without binary file
    meta = {
        "binaries": {
            "firecracker": {
                "binary_name": "firecracker",
                "binary_path": str(bin_dir / "firecracker-v1.15.0"),
                "full_version": "v1.15.0",
                "ci_version": "v1.15",
                "default_binary_path": str(bin_dir / "firecracker"),
                "is_default": 1,
            }
        },
        "kernels": {},
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "X " prefix in output
    assert "X " in result.output


def test_bin_ls_no_x_mark_for_existing_binary(tmp_path: Path, mocker):
    """Verify no X prefix when binary file exists."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    # Create metadata entry AND binary file
    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")

    meta = {
        "binaries": {
            "firecracker": {
                "binary_name": "firecracker",
                "binary_path": str(fc_bin),
                "full_version": "v1.15.0",
                "ci_version": "v1.15",
                "default_binary_path": str(bin_dir / "firecracker"),
                "is_default": 1,
            }
        },
        "kernels": {},
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
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
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel file with known size (~10MB)
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * (10 * 1024 * 1024))

    meta = {
        "kernels": {
            "a" * 64: {
                "filename": "vmlinux-fc-6.1.9-x86_64",
                "name": "vmlinux-fc-6.1.9-x86_64",
                "full_hash": "a" * 64,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size column shows formatted size (e.g., "10.0 MiB")
    assert "MiB" in result.output or "GiB" in result.output or "B" in result.output


def test_kernel_ls_size_format_bytes(tmp_path: Path, mocker):
    """Verify size formatting for small files (bytes)."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create small kernel file (< 1 MiB)
    kernel_file = kernels_dir / "vmlinux-small"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 512)  # 516 bytes

    meta = {
        "kernels": {
            "b" * 64: {
                "filename": "vmlinux-small",
                "name": "vmlinux-small",
                "full_hash": "b" * 64,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in bytes or appropriate unit
    assert "B" in result.output


def test_kernel_ls_size_format_mib(tmp_path: Path, mocker):
    """Verify size formatting for medium files (MiB)."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel file (1-1024 MiB)
    kernel_file = kernels_dir / "vmlinux-medium"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * (100 * 1024 * 1024))  # ~100 MiB

    meta = {
        "kernels": {
            "c" * 64: {
                "filename": "vmlinux-medium",
                "name": "vmlinux-medium",
                "full_hash": "c" * 64,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in MiB
    assert "MiB" in result.output


def test_kernel_ls_size_format_gib(tmp_path: Path, mocker):
    """Verify size formatting for large files (GiB)."""
    import json
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

    meta = {
        "kernels": {
            "d" * 64: {
                "filename": "vmlinux-large",
                "name": "vmlinux-large",
                "full_hash": "d" * 64,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify size shown in GiB
    assert "GiB" in result.output


def test_image_ls_shows_size_column(tmp_path: Path, mocker):
    """Verify image ls displays size column."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create image file with known size
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * (2 * 1024 * 1024 * 1024))  # 2 GiB

    full_hash = "e" * 64
    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": full_hash,
            }
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify size column shows formatted size
    assert "GiB" in result.output or "MiB" in result.output


def test_image_ls_size_various_units(tmp_path: Path, mocker):
    """Verify image ls shows sizes in various units."""
    import json
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

    meta = {
        "images": {
            "f" * 64: {
                "os_name": "Small Image",
                "filename": "small.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": "f" * 64,
            },
            "g" * 64: {
                "os_name": "Medium Image",
                "filename": "medium.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": "g" * 64,
            },
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    fake_images = [
        ImageSpec(
            id="small",
            image_type="test",
            version="1.0",
            name="Small Image",
            source="https://example.com/small.qcow2",
            format="qcow2",
            convert_to="ext4",
            size_mib=1,
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
            size_mib=100,
            sha256=None,
        ),
    ]

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=fake_images),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify size units are shown
    assert "B" in result.output or "MiB" in result.output or "GiB" in result.output


# ---------------------------------------------------------------------------
# Default prefix tests (Phase 4)
# ---------------------------------------------------------------------------


def test_kernel_ls_shows_default_prefix(tmp_path: Path, mocker):
    """Verify * prefix shown for default kernel."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel with is_default=true
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    full_hash = "h" * 64
    meta = {
        "kernels": {
            full_hash: {
                "filename": "vmlinux-fc-6.1.9-x86_64",
                "name": "vmlinux-fc-6.1.9-x86_64",
                "full_hash": full_hash,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
                "is_default": "true",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "* " prefix in output for default kernel
    assert "* " in result.output
    # Verify NO "Def" column in output
    assert "Def" not in result.output


def test_kernel_ls_no_prefix_for_non_default(tmp_path: Path, mocker):
    """Verify no * prefix for non-default kernel."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    # Create kernel with is_default=false
    kernel_file = kernels_dir / "vmlinux-fc-6.1.9-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    full_hash = "i" * 64
    meta = {
        "kernels": {
            full_hash: {
                "filename": "vmlinux-fc-6.1.9-x86_64",
                "name": "vmlinux-fc-6.1.9-x86_64",
                "full_hash": full_hash,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
                "is_default": "false",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
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
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    # Create image with is_default=true
    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    full_hash = "j" * 64
    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": full_hash,
                "is_default": "true",
            }
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "* " prefix in output
    assert "* " in result.output


def test_bin_ls_shows_default_prefix(tmp_path: Path, mocker):
    """Verify * prefix shown for default binary."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    # Create binary with is_default=true
    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")

    meta = {
        "binaries": {
            "firecracker": {
                "binary_name": "firecracker",
                "binary_path": str(fc_bin),
                "full_version": "v1.15.0",
                "ci_version": "v1.15",
                "default_binary_path": str(bin_dir / "firecracker"),
                "is_default": 1,
            }
        },
        "kernels": {},
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "* " prefix in output
    assert "* " in result.output


def test_kernel_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from kernel ls."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    kernels_dir = tmp_path / "kernels"
    kernels_dir.mkdir()

    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 1024)

    meta = {
        "kernels": {
            "k" * 64: {
                "filename": "vmlinux",
                "name": "vmlinux",
                "full_hash": "k" * 64,
                "version": "6.1.9",
                "type": "firecracker",
                "arch": "x86_64",
                "last_modified": "2026-01-01T12:00:00+00:00",
            }
        },
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["kernel", "ls", "--kernels-dir", str(kernels_dir)])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


def test_image_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from image ls."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    images_dir = tmp_path / "images"
    images_dir.mkdir()

    img_file = images_dir / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 1024)

    full_hash = "l" * 64
    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T12:00:00+00:00",
                "full_hash": full_hash,
            }
        },
        "kernels": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with (
        patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}),
        patch("mvmctl.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(images_dir)])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output


def test_bin_ls_no_def_column(tmp_path: Path, mocker):
    """Verify 'Def' column removed from bin ls."""
    import json
    import os

    cache_dir = tmp_path / "cache"
    cache_dir.mkdir(exist_ok=True)
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()

    fc_bin = bin_dir / "firecracker-v1.15.0"
    fc_bin.write_bytes(b"fake binary")

    meta = {
        "binaries": {
            "firecracker": {
                "binary_name": "firecracker",
                "binary_path": str(fc_bin),
                "full_version": "v1.15.0",
                "ci_version": "v1.15",
                "default_binary_path": str(bin_dir / "firecracker"),
                "is_default": 1,
            }
        },
        "kernels": {},
        "images": {},
    }
    (cache_dir / "metadata.json").write_text(json.dumps(meta))

    with patch.dict(os.environ, {"MVM_CACHE_DIR": str(cache_dir)}):
        result = click_runner.invoke(main_app, ["bin", "ls"])

    assert result.exit_code == 0
    # Verify "Def" column not in output
    assert "Def" not in result.output
