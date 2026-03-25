"""Tests for kernel download and build utilities."""

import subprocess
import tarfile
from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from mvmctl.core.kernel import (
    build_kernel,
    build_kernel_pipeline,
    configure_kernel,
    download_firecracker_config,
    download_kernel_source,
    extract_kernel_tarball,
    run_make,
)
from mvmctl.exceptions import ChecksumMismatchError, KernelError


def test_download_kernel_source_success(tmp_path: Path, mocker):
    import hashlib

    fake_body = b"kernel-data-here"
    expected_sha256 = hashlib.sha256(fake_body).hexdigest()
    mock_response = MagicMock()
    mock_response.read.side_effect = [fake_body, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        download_kernel_source(
            "https://example.com/kernel.tar.xz", dest, expected_sha256=expected_sha256
        )

    assert dest.exists()
    assert dest.read_bytes() == fake_body


def test_download_kernel_source_checksum_match(tmp_path: Path):
    import hashlib

    data = b"some-kernel-bytes"
    expected = hashlib.sha256(data).hexdigest()

    mock_response = MagicMock()
    mock_response.read.side_effect = [data, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        download_kernel_source("https://example.com/k.tar.xz", dest, expected)

    assert dest.exists()


def test_download_kernel_source_checksum_mismatch(tmp_path: Path):
    data = b"some-kernel-bytes"

    mock_response = MagicMock()
    mock_response.read.side_effect = [data, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", return_value=mock_response):
        with pytest.raises(ChecksumMismatchError):
            download_kernel_source("https://example.com/k.tar.xz", dest, "deadbeef")

    assert not dest.exists()


def test_download_kernel_source_url_error(tmp_path: Path):
    dest = tmp_path / "linux.tar.xz"
    with patch("mvmctl.utils.http.urlopen", side_effect=URLError("no network")):
        with pytest.raises(KernelError):
            download_kernel_source(
                "https://example.com/k.tar.xz", dest, expected_sha256="abcd1234" * 8
            )


def test_extract_kernel_tarball_success(tmp_path: Path):
    tarball = tmp_path / "linux-6.1.tar.xz"
    extract_dir = tmp_path / "build"
    extract_dir.mkdir()

    inner_dir = tmp_path / "staging" / "linux-6.1"
    inner_dir.mkdir(parents=True)
    (inner_dir / "Makefile").write_text("all:")

    with tarfile.open(tarball, "w:xz") as tar:
        tar.add(inner_dir.parent / "linux-6.1", arcname="linux-6.1")

    result = extract_kernel_tarball(tarball, extract_dir)

    assert result is not None
    assert result.name == "linux-6.1"
    assert (result / "Makefile").exists()


def test_extract_kernel_tarball_bad_file(tmp_path: Path):
    tarball = tmp_path / "bad.tar.xz"
    tarball.write_bytes(b"not-a-tarball")

    with pytest.raises(KernelError):
        extract_kernel_tarball(tarball, tmp_path / "out")


def test_extract_kernel_tarball_no_linux_dir(tmp_path: Path):
    tarball = tmp_path / "bad.tar.xz"
    extract_dir = tmp_path / "build"
    extract_dir.mkdir()

    inner_dir = tmp_path / "staging" / "other-dir"
    inner_dir.mkdir(parents=True)
    (inner_dir / "Makefile").write_text("all:")

    with tarfile.open(tarball, "w:xz") as tar:
        tar.add(inner_dir.parent / "other-dir", arcname="other-dir")

    with pytest.raises(KernelError):
        extract_kernel_tarball(tarball, extract_dir)


def test_run_make_capture(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = "ok"
    mock_result.stderr = ""

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
        code, stdout, stderr = run_make(tmp_path, "vmlinux", jobs=4, capture_output=True)

    assert code == 0
    assert stdout == "ok"
    mock_run.assert_called_once_with(
        ["make", "vmlinux", "-j4"],
        cwd=tmp_path,
        capture_output=True,
        text=True,
    )


def test_run_make_no_capture(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 0

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result):
        code, stdout, stderr = run_make(tmp_path, "defconfig", jobs=1, capture_output=False)

    assert code == 0
    assert stdout == ""
    assert stderr == ""


def test_run_make_failure(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 2
    mock_result.stdout = ""
    mock_result.stderr = "error"

    with patch("mvmctl.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
        code, stdout, stderr = run_make(tmp_path, "vmlinux", jobs=4, capture_output=True)

    assert code == 2
    assert stdout == ""
    assert stderr == "error"
    mock_run.assert_called_once_with(
        ["make", "vmlinux", "-j4"],
        cwd=tmp_path,
        capture_output=True,
        text=True,
    )


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_success(mock_urlopen: MagicMock, tmp_path: Path):
    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    download_firecracker_config(kernel_dir, version="6.1.102")

    config_path = kernel_dir / ".config"
    assert config_path.exists()
    assert config_path.read_text() == config_content


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_uses_yaml_template(mock_urlopen: MagicMock, tmp_path: Path):
    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    download_firecracker_config(kernel_dir, version="6.1.102")

    request = mock_urlopen.call_args[0][0]
    assert request.full_url.endswith("microvm-kernel-ci-x86_64-6.1.config")


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_supports_version_placeholder(
    mock_urlopen: MagicMock, tmp_path: Path
):
    from mvmctl.models.kernel import KernelSpec

    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    spec = KernelSpec(
        name="kernel-firecracker-custom",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.invalid/vmlinux-{version}",
        output_name="vmlinux",
        build_dir="/tmp/build",
        config_url_template="https://example.invalid/microvm-kernel-{version}.config",
    )

    download_firecracker_config(kernel_dir, version="6.1.102", kernel_spec=spec)

    request = mock_urlopen.call_args[0][0]
    assert request.full_url.endswith("microvm-kernel-6.1.config")


@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_config_url_error(mock_urlopen: MagicMock, tmp_path: Path):
    mock_urlopen.side_effect = URLError("Connection refused")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        download_firecracker_config(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.download_firecracker_config")
@patch("mvmctl.core.kernel.subprocess.run")
def test_configure_kernel_success(mock_run: MagicMock, mock_download: MagicMock, tmp_path: Path):
    mock_download.return_value = True

    def mock_run_side_effect(cmd, **kwargs):
        mock_result = MagicMock()
        mock_result.returncode = 0
        return mock_result

    mock_run.side_effect = mock_run_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_download_falls_back_to_defconfig(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = False

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "defconfig":
            return (0, "", "")
        elif target == "olddefconfig":
            return (0, "", "")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    with patch("mvmctl.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_olddefconfig_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = True

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "olddefconfig":
            return (1, "", "")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_defconfig_also_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.side_effect = KernelError("download failed")
    mock_run_make.return_value = (1, "", "defconfig error")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    with pytest.raises(KernelError):
        configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.run_make")
@patch("mvmctl.core.kernel.download_firecracker_config")
def test_configure_kernel_second_olddefconfig_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = True

    call_count = [0]

    def mock_run_make_side_effect(kernel_dir, target, **kwargs):
        if target == "olddefconfig":
            call_count[0] += 1
            if call_count[0] == 1:
                return (0, "", "")
            return (1, "", "failed")
        return (0, "", "")

    mock_run_make.side_effect = mock_run_make_side_effect

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text(
        "CONFIG_BTRFS_FS=y\nCONFIG_VIRTIO_BLK=y\nCONFIG_VIRTIO_NET=y\n"
        "CONFIG_SERIAL_8250_CONSOLE=y\nCONFIG_KVM_GUEST=y\n"
    )

    with patch("mvmctl.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        with pytest.raises(KernelError):
            configure_kernel(kernel_dir, version="6.1.102")


@patch("mvmctl.core.kernel.download_firecracker_config")
@patch("mvmctl.core.kernel.subprocess.run")
def test_configure_kernel_missing_required_settings(
    mock_run: MagicMock, mock_download: MagicMock, tmp_path: Path
):
    mock_download.return_value = True
    mock_run.return_value = MagicMock(returncode=0)

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/sh\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text("CONFIG_BTRFS_FS=y\n")

    with patch("typer.confirm", return_value=False):
        with pytest.raises(KernelError):
            configure_kernel(kernel_dir, version="6.1.102")


def test_build_kernel_success(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc) as mock_popen:
        build_kernel(kernel_dir, output_path, jobs=2)

    assert output_path.exists()
    assert mock_popen.call_args.kwargs["stdout"] is not None


def test_build_kernel_failure(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 2

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=1)


def test_build_kernel_failure_with_error_lines(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 2

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=1)


def test_build_kernel_vmlinux_not_found(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    with patch("mvmctl.core.kernel.subprocess.Popen", return_value=mock_proc):
        with pytest.raises(KernelError):
            build_kernel(kernel_dir, output_path, jobs=2)


def test_build_kernel_pipeline_cached(tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"cached-kernel")

    from mvmctl.core.kernel import _compute_config_hash

    config_hash = _compute_config_hash("6.1.102", None)
    cache_key = f"6.1.102-{config_hash}"
    cache_marker = tmp_path / f"kernel-cache-{cache_key}.marker"
    cache_marker.write_text(cache_key)

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=tmp_path / "build",
    )


def test_build_kernel_pipeline_download_fails(tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    with patch(
        "mvmctl.core.kernel.download_kernel_source",
        side_effect=KernelError("download failed"),
    ):
        with pytest.raises(KernelError):
            build_kernel_pipeline(
                version="6.1.102",
                source_url="https://example.com/linux.tar.xz",
                output_path=output_path,
                build_dir=build_dir,
            )


@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value=None)
def test_build_kernel_pipeline_requires_checksum(mock_fetch_sha256: MagicMock, tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    with pytest.raises(KernelError, match="Checksum required"):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_full_success(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_download.assert_called_once()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()


@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_extract_fails(
    mock_download: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    mock_download.return_value = True
    mock_extract.side_effect = KernelError("extraction failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_configure_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.side_effect = KernelError("configure failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_build_fails(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.side_effect = KernelError("build failed")

    with pytest.raises(KernelError):
        build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_download.assert_not_called()
    mock_extract.assert_not_called()


@patch("mvmctl.core.kernel.shutil.copy2")
@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball_needs_extract(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    mock_copy2: MagicMock,
    tmp_path: Path,
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    kernel_src_dir = build_dir / "linux-6.1.102"

    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
        jobs=4,
    )

    mock_download.assert_not_called()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()


def test_configure_kernel_missing_settings_prompt(tmp_path: Path):
    """P2-10: configure_kernel shows confirmation prompt for missing required settings."""
    # Create a kernel dir with a config missing required settings
    kernel_dir = tmp_path / "linux-6.1.102"
    kernel_dir.mkdir()
    scripts = kernel_dir / "scripts"
    scripts.mkdir()
    (scripts / "config").write_text("#!/bin/sh")
    (scripts / "config").chmod(0o755)
    (kernel_dir / ".config").write_text("# minimal config\nCONFIG_SOMETHING=y\n")

    with (
        patch("mvmctl.core.kernel.download_firecracker_config"),
        patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
        patch("mvmctl.core.kernel.subprocess.run", return_value=MagicMock(returncode=0)),
        patch("typer.confirm", return_value=False) as mock_confirm,
    ):
        with pytest.raises(KernelError, match="Required kernel settings are missing"):
            configure_kernel(kernel_dir, "6.1.102")
        mock_confirm.assert_called_once()


def test_configure_kernel_missing_settings_proceed(tmp_path: Path):
    """P2-10: configure_kernel proceeds when user confirms despite missing settings."""
    # Create a kernel dir with a config missing required settings
    kernel_dir = tmp_path / "linux-6.1.102"
    kernel_dir.mkdir()
    scripts = kernel_dir / "scripts"
    scripts.mkdir()
    (scripts / "config").write_text("#!/bin/sh")
    (scripts / "config").chmod(0o755)
    (kernel_dir / ".config").write_text("# minimal config\nCONFIG_SOMETHING=y\n")

    with (
        patch("mvmctl.core.kernel.download_firecracker_config"),
        patch("mvmctl.core.kernel.run_make", return_value=(0, "", "")),
        patch("mvmctl.core.kernel.subprocess.run", return_value=MagicMock(returncode=0)),
        patch("typer.confirm", return_value=True) as mock_confirm,
    ):
        # Should not raise when user confirms
        configure_kernel(kernel_dir, "6.1.102")
        mock_confirm.assert_called_once()


# ---------------------------------------------------------------------------
# Issue #16: Kernel Build Log Accumulation - File-based logging
# ---------------------------------------------------------------------------


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_kernel_uses_file_based_logging(mock_popen: MagicMock, tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0
    mock_popen.return_value = mock_proc

    build_kernel(kernel_dir, output_path, jobs=2, build_log_path=log_path)

    mock_popen.assert_called_once()
    call_kwargs = mock_popen.call_args[1]
    assert hasattr(call_kwargs.get("stdout"), "write")
    assert call_kwargs.get("stderr") == subprocess.STDOUT


@patch("mvmctl.core.kernel.subprocess.Popen")
def test_build_kernel_post_processes_log_file(mock_popen: MagicMock, tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"
    log_path = tmp_path / "build.log"

    log_content = "CC init/main.o\nWARNING: something\nERROR: undefined\nLD vmlinux\n"

    mock_proc = MagicMock()
    mock_proc.wait.return_value = 0

    def fake_popen(cmd, cwd=None, stdout=None, stderr=None, **kwargs):
        if stdout and hasattr(stdout, "write"):
            stdout.write(log_content)
            stdout.flush()
        return mock_proc

    mock_popen.side_effect = fake_popen

    with patch("mvmctl.core.kernel.logger.debug") as mock_debug:
        build_kernel(kernel_dir, output_path, jobs=2, build_log_path=log_path)

    assert log_path.exists()
    assert mock_debug.call_count >= 1


# ---------------------------------------------------------------------------
# Issue #18: Kernel Build Cache - Config hash
# ---------------------------------------------------------------------------


def test_compute_config_hash_consistent():
    """Test that _compute_config_hash returns consistent hash for same inputs."""
    from mvmctl.core.kernel import _compute_config_hash

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.1.102", None)

    assert hash1 == hash2
    assert len(hash1) == 16  # First 16 chars of SHA256


def test_compute_config_hash_different_versions():
    """Test that _compute_config_hash returns different hashes for different versions."""
    from mvmctl.core.kernel import _compute_config_hash

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.2.0", None)

    assert hash1 != hash2


def test_compute_config_hash_with_user_config(tmp_path: Path):
    """Test that _compute_config_hash includes user config content."""
    from mvmctl.core.kernel import _compute_config_hash

    user_config = tmp_path / "user.config"
    user_config.write_text("CONFIG_CUSTOM=y\n")

    hash1 = _compute_config_hash("6.1.102", None)
    hash2 = _compute_config_hash("6.1.102", user_config)

    assert hash1 != hash2


@patch("mvmctl.core.kernel.fetch_kernel_sha256", return_value="fakechecksum256fake")
@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_uses_cache_marker(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    mock_fetch_sha256: MagicMock,
    tmp_path: Path,
):
    """Test that build_kernel_pipeline creates cache marker with config hash."""
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    kernel_src_dir = build_dir / "linux-6.1.102"
    kernel_src_dir.mkdir()

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True

    def create_vmlinux(src_dir: Path, out_path: Path, jobs: int) -> None:
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_bytes(b"\x7fELF")

    mock_build.side_effect = create_vmlinux

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    markers = list(tmp_path.glob("kernel-cache-*.marker"))
    assert len(markers) == 1
    cached_kernels = list(tmp_path.glob("kernel-cache-*.vmlinux"))
    assert len(cached_kernels) == 1


@patch("mvmctl.core.kernel.build_kernel")
@patch("mvmctl.core.kernel.configure_kernel")
@patch("mvmctl.core.kernel.extract_kernel_tarball")
@patch("mvmctl.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_skips_build_if_cache_matches(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
    tmp_path: Path,
):
    """Test that build_kernel_pipeline skips build if cache marker exists."""
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"\x7fELF")  # Pre-create kernel
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    from mvmctl.core.kernel import _compute_config_hash

    config_hash = _compute_config_hash("6.1.102", None)
    cache_marker = tmp_path / f"kernel-cache-6.1.102-{config_hash}.marker"
    cache_marker.write_text(f"6.1.102-{config_hash}")
    cached_kernel = tmp_path / f"kernel-cache-6.1.102-{config_hash}.vmlinux"
    cached_kernel.write_bytes(b"cached artifact")

    output_path.unlink()

    build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    mock_build.assert_not_called()
    mock_configure.assert_not_called()
    mock_extract.assert_not_called()
    mock_download.assert_not_called()
    assert output_path.read_bytes() == b"cached artifact"
