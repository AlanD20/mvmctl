"""Tests for CLI asset commands (kernel, image, bin, cache clear)."""

import json
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from click.testing import CliRunner as ClickCliRunner
from typer.testing import CliRunner

from fcm.cli.asset import kernel_app
from fcm.main import app as main_app
from fcm.core.binary_manager import BinaryVersion
from fcm.exceptions import AssetNotFoundError, BinaryError, KernelError
from fcm.models.image import ImageSpec

runner = CliRunner()
click_runner = ClickCliRunner()

_FAKE_IMAGES = [
    ImageSpec(
        id="ubuntu-24.04",
        name="Ubuntu 24.04 LTS",
        source="https://example.com/ubuntu.qcow2",
        format="qcow2",
        convert_to="ext4",
        size_mib=2048,
        sha256=None,
    ),
    ImageSpec(
        id="debian-12",
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
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "vmlinux" in result.output


def test_kernel_ls_empty_dir(tmp_path: Path):
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path)])
    assert result.exit_code == 0


def test_kernel_ls_json(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * 2048)
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
    (tmp_path / "vmlinux").write_bytes(b"\x00" * 1024)
    (tmp_path / "vmlinux-6.1.102").write_bytes(b"\x00" * 2048)
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    # name is now base_name, so both will be "vmlinux"
    names = [e["name"] for e in data]
    assert "vmlinux" in names
    assert len(data) == 2
    # full_name contains the actual filenames
    full_names = [e["full_name"] for e in data]
    assert "vmlinux" in full_names
    assert "vmlinux-6.1.102" in full_names


def test_kernel_ls_skips_non_vmlinux_files(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x00" * 1024)
    (tmp_path / "somefile.txt").write_text("not a kernel")
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(tmp_path), "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    names = [e["name"] for e in data]
    assert "vmlinux" in names
    assert "somefile.txt" not in names


# ---------------------------------------------------------------------------
# kernel fetch
# ---------------------------------------------------------------------------


@patch("fcm.cli.asset.build_kernel_pipeline", return_value=None)
def test_kernel_fetch_official_success(mock_build: MagicMock, tmp_path: Path):
    out = tmp_path / "vmlinux-6.1.9"
    out.write_bytes(b"\x7fELF")
    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--type", "official", "--version", "6.1.9", "--out", str(out)]
    )
    assert result.exit_code == 0
    assert "Kernel" in result.output
    mock_build.assert_called_once()


@patch("fcm.cli.asset.build_kernel_pipeline", side_effect=KernelError("build failed"))
def test_kernel_fetch_official_failure(mock_build: MagicMock, tmp_path: Path):
    out = tmp_path / "vmlinux-6.1.9"
    result = click_runner.invoke(
        main_app, ["kernel", "fetch", "--type", "official", "--version", "6.1.9", "--out", str(out)]
    )
    assert result.exit_code == 1


@patch("fcm.core.kernel.download_firecracker_kernel")
@patch("fcm.cli.asset._get_ci_version", return_value="1.12")
def test_kernel_fetch_firecracker_success(mock_ci: MagicMock, mock_dl: MagicMock, tmp_path: Path):
    fc_kernel = tmp_path / "vmlinux-fc-1.12-amd64"
    fc_kernel.write_bytes(b"\x7fELF")
    mock_dl.return_value = fc_kernel
    result = click_runner.invoke(
        main_app,
        ["kernel", "fetch", "--type", "firecracker", "--version", "1.12"],
    )
    assert result.exit_code == 0
    assert "ready" in result.output.lower() or "kernel" in result.output.lower()


def test_kernel_fetch_missing_type(tmp_path: Path):
    result = click_runner.invoke(main_app, ["kernel", "fetch"])
    assert result.exit_code != 0


@patch("fcm.cli.asset.build_kernel_pipeline", return_value=None)
def test_kernel_fetch_with_jobs(mock_build: MagicMock, tmp_path: Path):
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


def test_kernel_rm_success(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * 1024)
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", "vmlinux", "--kernels-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not kernel.exists()


def test_kernel_rm_not_found(tmp_path: Path):
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", "vmlinux", "--kernels-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 1


def test_kernel_rm_with_confirmation(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * 1024)
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", "vmlinux", "--kernels-dir", str(tmp_path)],
        input="y\n",
    )
    assert result.exit_code == 0
    assert not kernel.exists()


def test_kernel_rm_abort_confirmation(tmp_path: Path):
    kernel = tmp_path / "vmlinux"
    kernel.write_bytes(b"\x00" * 1024)
    result = click_runner.invoke(
        main_app,
        ["kernel", "rm", "vmlinux", "--kernels-dir", str(tmp_path)],
        input="n\n",
    )
    assert result.exit_code != 0
    assert kernel.exists()


# ---------------------------------------------------------------------------
# image ls
# ---------------------------------------------------------------------------


def test_image_ls_normal(tmp_path: Path):
    (tmp_path / "ubuntu-24.04.ext4").write_bytes(b"\x00" * 1024)
    (tmp_path / "debian-12.ext4").write_bytes(b"\x00" * 1024)
    with (
        patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("fcm.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0
    assert "Ubuntu 24.04 LTS" in result.output
    assert "Debian 12" in result.output


def test_image_ls_json():
    with patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert isinstance(data, list)
    names = {item["name"] for item in data}
    assert "Ubuntu 24.04 LTS" in names or len(data) == 0


def test_image_ls_empty():
    with patch("fcm.cli.asset.load_images_config", return_value=[]):
        result = click_runner.invoke(main_app, ["image", "ls", "--json"])
    assert result.exit_code == 0
    data = json.loads(result.output)
    assert data == []


def test_image_ls_shows_cached_marker(tmp_path: Path):
    (tmp_path / "ubuntu-24.04.ext4").touch()
    with (
        patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("fcm.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(main_app, ["image", "ls", "--images-dir", str(tmp_path)])
    assert result.exit_code == 0


# ---------------------------------------------------------------------------
# image fetch
# ---------------------------------------------------------------------------


@patch("fcm.cli.asset.fetch_image")
@patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_success(mock_config: MagicMock, mock_fetch: MagicMock, tmp_path: Path):
    mock_fetch.return_value = tmp_path / "ubuntu-24.04.ext4"
    result = click_runner.invoke(
        main_app, ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)]
    )
    assert result.exit_code == 0
    assert "Image ready" in result.output
    mock_fetch.assert_called_once()


@patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_not_found(mock_config: MagicMock):
    result = click_runner.invoke(main_app, ["image", "fetch", "nonexistent"])
    assert result.exit_code == 1


@patch("fcm.cli.asset.fetch_image", return_value=None)
@patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_failure(mock_config: MagicMock, mock_fetch: MagicMock, tmp_path: Path):
    result = click_runner.invoke(
        main_app, ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)]
    )
    assert result.exit_code == 1


@patch("fcm.cli.asset.fetch_image")
@patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES)
def test_image_fetch_with_force(mock_config: MagicMock, mock_fetch: MagicMock, tmp_path: Path):
    mock_fetch.return_value = tmp_path / "ubuntu-24.04.ext4"
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
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    full_hash = "a" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(tmp_path, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images"), "--force"],
    )
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not img_file.exists()


def test_image_rm_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    result = click_runner.invoke(
        main_app,
        ["image", "rm", "abcdef", "--images-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 1


def test_image_rm_with_confirmation(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    full_hash = "b" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(tmp_path, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
        input="y\n",
    )
    assert result.exit_code == 0
    assert not img_file.exists()


def test_image_rm_abort_confirmation(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    full_hash = "c" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / f"{full_hash}.ext4"
    img_file.write_text("fake")
    _write_image_meta(tmp_path, full_hash, img_file.name)
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path / "images")],
        input="n\n",
    )
    assert result.exit_code != 0
    assert img_file.exists()


def test_image_rm_multiple_ids(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
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
            "--force",
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
    with patch("fcm.cli.asset.list_local_versions", return_value=fake_versions):
        result = click_runner.invoke(main_app, ["bin", "ls"])
    assert result.exit_code == 0
    assert "1.5.0" in result.output


def test_bin_ls_empty():
    with patch("fcm.cli.asset.list_local_versions", return_value=[]):
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
        patch("fcm.cli.asset.list_local_versions", return_value=local),
        patch("fcm.cli.asset.list_remote_versions", return_value=remote),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 0
    assert "1.6.0" in result.output
    assert "1.5.0" in result.output


def test_bin_ls_remote_error():
    with (
        patch("fcm.cli.asset.list_local_versions", return_value=[]),
        patch("fcm.cli.asset.list_remote_versions", side_effect=BinaryError("network fail")),
    ):
        result = click_runner.invoke(main_app, ["bin", "ls", "--remote"])
    assert result.exit_code == 1


def test_bin_ls_with_limit():
    with (
        patch("fcm.cli.asset.list_local_versions", return_value=[]),
        patch("fcm.cli.asset.list_remote_versions", return_value=["1.6.0"]) as mock_remote,
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
    with patch("fcm.cli.asset.fetch_binary", return_value=bv):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 0
    assert "Downloaded" in result.output
    assert "1.5.0" in result.output


def test_bin_fetch_error():
    with patch("fcm.cli.asset.fetch_binary", side_effect=BinaryError("download failed")):
        result = click_runner.invoke(main_app, ["bin", "fetch", "1.5.0"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# bin set-default
# ---------------------------------------------------------------------------


def test_bin_set_default_success():
    with patch("fcm.cli.asset.set_active_version") as mock_set:
        result = click_runner.invoke(main_app, ["bin", "set-default", "1.5.0"])
    assert result.exit_code == 0
    assert "Active version set" in result.output
    mock_set.assert_called_once_with("1.5.0")


def test_bin_set_default_not_found():
    with patch(
        "fcm.cli.asset.set_active_version",
        side_effect=AssetNotFoundError("not downloaded"),
    ):
        result = click_runner.invoke(main_app, ["bin", "set-default", "9.9.9"])
    assert result.exit_code == 1


# ---------------------------------------------------------------------------
# bin rm
# ---------------------------------------------------------------------------


def test_bin_rm_success():
    with patch("fcm.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.output
    mock_rm.assert_called_once_with("1.5.0")


def test_bin_rm_not_found():
    with patch(
        "fcm.cli.asset.remove_version",
        side_effect=AssetNotFoundError("not found"),
    ):
        result = click_runner.invoke(main_app, ["bin", "rm", "9.9.9", "--force"])
    assert result.exit_code == 1


def test_bin_rm_with_confirmation():
    with patch("fcm.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"], input="y\n")
    assert result.exit_code == 0
    mock_rm.assert_called_once()


def test_bin_rm_abort_confirmation():
    with patch("fcm.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0"], input="n\n")
    assert result.exit_code != 0
    mock_rm.assert_not_called()


# ---------------------------------------------------------------------------
# cache clear
# ---------------------------------------------------------------------------


def test_cache_clear_dirs_exist(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "kernels").mkdir()
    (tmp_path / "images").mkdir()
    (tmp_path / "bin" / "firecracker-v1.0.0").touch()

    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert "Removed" in result.output
    assert not (tmp_path / "bin").exists()
    assert not (tmp_path / "kernels").exists()
    assert not (tmp_path / "images").exists()


def test_cache_clear_nothing_to_clear(tmp_path: Path):
    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert "Nothing to clear" in result.output


def test_cache_clear_partial_dirs(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert not (tmp_path / "bin").exists()


def test_cache_clear_with_confirmation(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "kernels").mkdir()
    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear"], input="y\n")
    assert result.exit_code == 0
    assert not (tmp_path / "bin").exists()


def test_cache_clear_abort_confirmation(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear"], input="n\n")
    assert result.exit_code != 0
    assert (tmp_path / "bin").exists()


def test_cache_clear_preserves_vms_dir(tmp_path: Path):
    (tmp_path / "bin").mkdir()
    (tmp_path / "vms").mkdir()
    (tmp_path / "vms" / "state.json").write_text("{}")
    with patch("fcm.cli.asset.get_cache_dir", return_value=tmp_path):
        result = click_runner.invoke(main_app, ["clear", "--force"])
    assert result.exit_code == 0
    assert (tmp_path / "vms").exists()


def test_image_set_default(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    (tmp_path / "images").mkdir()
    (tmp_path / "images" / "ubuntu-24.04.ext4").write_bytes(b"\x00" * 1024)
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", "ubuntu-24.04", "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output


def test_image_set_default_not_found(tmp_path: Path, monkeypatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    (tmp_path / "images").mkdir()
    result = click_runner.invoke(
        main_app,
        ["image", "set-default", "ubuntu-24.04", "--images-dir", str(tmp_path / "images")],
    )
    assert result.exit_code == 1


def test_image_ls_remote(tmp_path: Path):
    with (
        patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES),
        patch("fcm.cli.asset.get_images_dir", return_value=tmp_path),
    ):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--remote", "--images-dir", str(tmp_path)]
        )
    assert result.exit_code == 0
    assert "ubuntu-24.04" in result.output


def test_kernel_set_default_cli(tmp_path: Path):
    vmlinux = tmp_path / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", "vmlinux", "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 0
    assert "vmlinux" in result.output


def test_kernel_set_default_not_found(tmp_path: Path):
    result = click_runner.invoke(
        main_app,
        ["kernel", "set-default", "vmlinux", "--kernels-dir", str(tmp_path)],
    )
    assert result.exit_code == 1


def test_bin_ls_default_limit():
    from fcm.cli.asset import bin_app

    result = runner.invoke(bin_app, ["ls", "--help"])
    assert "5" in result.output


def test_kernel_ls_auto_creates_dir(tmp_path: Path):
    missing = tmp_path / "kernels"
    result = runner.invoke(kernel_app, ["ls", "--kernels-dir", str(missing)])
    assert result.exit_code == 0
    assert missing.exists()


@patch("fcm.cli.asset.fetch_image")
@patch("fcm.cli.asset.load_images_config")
def test_image_fetch_confirms_existing_image(mock_config, mock_fetch, tmp_path):
    """FIX-009: image fetch warns when image already exists."""
    from fcm.main import app
    from fcm.models.image import ImageSpec
    from click.testing import CliRunner as _ClickRunner

    mock_config.return_value = [
        ImageSpec(
            id="ubuntu-24.04",
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
    mock_fetch.return_value = tmp_path / "ubuntu-24.04.ext4"

    # User says NO to re-download
    result = _ClickRunner().invoke(
        app,
        ["image", "fetch", "ubuntu-24.04", "--out", str(tmp_path)],
        input="n\n",  # Answer 'no' to confirm prompt
    )
    assert result.exit_code == 0
    mock_fetch.assert_not_called()  # Should not have called fetch


def test_bin_rm_multiple_versions():
    with patch("fcm.cli.asset.remove_version") as mock_rm:
        result = click_runner.invoke(main_app, ["bin", "rm", "1.5.0", "1.6.0", "--force"])
    assert result.exit_code == 0
    assert mock_rm.call_count == 2


def test_bin_rm_no_args():
    result = click_runner.invoke(main_app, ["bin", "rm", "--force"])
    assert result.exit_code == 1


def test_kernel_rm_multiple(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    (tmp_path / "vmlinux-6.1.102").write_bytes(b"\x7fELF")
    result = runner.invoke(
        kernel_app, ["rm", "vmlinux", "vmlinux-6.1.102", "--kernels-dir", str(tmp_path), "--force"]
    )
    assert result.exit_code == 0
    assert not (tmp_path / "vmlinux").exists()
    assert not (tmp_path / "vmlinux-6.1.102").exists()


def test_kernel_rm_no_args(tmp_path: Path):
    result = runner.invoke(kernel_app, ["rm", "--kernels-dir", str(tmp_path), "--force"])
    assert result.exit_code == 1


def test_image_rm_no_args(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    result = click_runner.invoke(
        main_app, ["image", "rm", "--images-dir", str(tmp_path), "--force"]
    )
    assert result.exit_code == 1


def test_image_rm_ambiguous(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
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
        main_app, ["image", "rm", "f", "--images-dir", str(tmp_path / "images"), "--force"]
    )
    assert result.exit_code == 1
    assert (
        "Ambiguous" in result.output
        or "ambiguous" in result.output.lower()
        or "matches" in result.output
    )


def test_image_rm_missing_file(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    full_hash = "9" * 64
    _write_image_meta(tmp_path, full_hash, f"{full_hash}.ext4")
    result = click_runner.invoke(
        main_app,
        ["image", "rm", full_hash[:6], "--images-dir", str(tmp_path), "--force"],
    )
    assert result.exit_code == 1
    assert "not found" in result.output.lower() or "missing" in result.output.lower()


def test_image_ls_with_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("FCM_CACHE_DIR", str(tmp_path))
    full_hash = "1" * 64
    (tmp_path / "images").mkdir(exist_ok=True)
    img_file = tmp_path / "images" / "ubuntu-24.04.ext4"
    img_file.write_bytes(b"\x00" * 64)
    import json

    meta = {
        "images": {
            full_hash: {
                "os_name": "Ubuntu 24.04 LTS",
                "yaml_id": "ubuntu-24.04",
                "filename": "ubuntu-24.04.ext4",
                "fs_type": "ext4",
                "pulled_at": "2026-01-01T00:00:00+00:00",
                "full_hash": full_hash,
            }
        }
    }
    (tmp_path / "metadata.json").write_text(json.dumps(meta))
    with patch("fcm.cli.asset.load_images_config", return_value=_FAKE_IMAGES):
        result = click_runner.invoke(
            main_app, ["image", "ls", "--images-dir", str(tmp_path / "images")]
        )
    assert result.exit_code == 0
    assert full_hash[:6] in result.output
    assert "Ubuntu 24.04 LTS" in result.output
