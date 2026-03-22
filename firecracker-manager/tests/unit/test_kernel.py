"""Tests for kernel download and build utilities."""

import tarfile
from pathlib import Path
from unittest.mock import patch, MagicMock
from urllib.error import URLError

from fcm.core.kernel import (
    build_kernel,
    build_kernel_pipeline,
    configure_kernel,
    download_firecracker_config,
    download_kernel_source,
    extract_kernel_tarball,
    run_make,
)


def test_download_kernel_source_success(tmp_path: Path):
    fake_body = b"kernel-data-here"
    mock_response = MagicMock()
    mock_response.read.side_effect = [fake_body, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("fcm.core.kernel.urlopen", return_value=mock_response):
        result = download_kernel_source("https://example.com/kernel.tar.xz", dest)

    assert result is True
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
    with patch("fcm.core.kernel.urlopen", return_value=mock_response):
        result = download_kernel_source("https://example.com/k.tar.xz", dest, expected)

    assert result is True
    assert dest.exists()


def test_download_kernel_source_checksum_mismatch(tmp_path: Path):
    data = b"some-kernel-bytes"

    mock_response = MagicMock()
    mock_response.read.side_effect = [data, b""]
    mock_response.headers = {"Content-Length": None}
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)

    dest = tmp_path / "linux.tar.xz"
    with patch("fcm.core.kernel.urlopen", return_value=mock_response):
        result = download_kernel_source("https://example.com/k.tar.xz", dest, "deadbeef")

    assert result is False
    assert not dest.exists()


def test_download_kernel_source_url_error(tmp_path: Path):
    dest = tmp_path / "linux.tar.xz"
    with patch("fcm.core.kernel.urlopen", side_effect=URLError("no network")):
        result = download_kernel_source("https://example.com/k.tar.xz", dest)

    assert result is False


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

    result = extract_kernel_tarball(tarball, tmp_path / "out")
    assert result is None


def test_extract_kernel_tarball_no_linux_dir(tmp_path: Path):
    tarball = tmp_path / "bad.tar.xz"
    extract_dir = tmp_path / "build"
    extract_dir.mkdir()

    inner_dir = tmp_path / "staging" / "other-dir"
    inner_dir.mkdir(parents=True)
    (inner_dir / "Makefile").write_text("all:")

    with tarfile.open(tarball, "w:xz") as tar:
        tar.add(inner_dir.parent / "other-dir", arcname="other-dir")

    result = extract_kernel_tarball(tarball, extract_dir)

    assert result is None


def test_run_make_capture(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = "ok"
    mock_result.stderr = ""

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
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

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result):
        code, stdout, stderr = run_make(tmp_path, "defconfig", jobs=1, capture_output=False)

    assert code == 0
    assert stdout == ""
    assert stderr == ""


def test_run_make_failure(tmp_path: Path):
    mock_result = MagicMock()
    mock_result.returncode = 2
    mock_result.stdout = ""
    mock_result.stderr = "error"

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result) as mock_run:
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


@patch("fcm.core.kernel.urlopen")
def test_download_firecracker_config_success(mock_urlopen: MagicMock, tmp_path: Path):
    config_content = "CONFIG_TEST=y\n"
    mock_response = MagicMock()
    mock_response.read.return_value = config_content.encode("utf-8")
    mock_response.__enter__ = lambda s: s
    mock_response.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_response

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    result = download_firecracker_config(kernel_dir)

    assert result is True
    config_path = kernel_dir / ".config"
    assert config_path.exists()
    assert config_path.read_text() == config_content


@patch("fcm.core.kernel.urlopen")
def test_download_firecracker_config_url_error(mock_urlopen: MagicMock, tmp_path: Path):
    mock_urlopen.side_effect = URLError("Connection refused")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    result = download_firecracker_config(kernel_dir)

    assert result is False


@patch("fcm.core.kernel.download_firecracker_config")
@patch("fcm.core.kernel.subprocess.run")
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

    result = configure_kernel(kernel_dir)

    assert result is True


@patch("fcm.core.kernel.run_make")
@patch("fcm.core.kernel.download_firecracker_config")
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

    with patch("fcm.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        result = configure_kernel(kernel_dir)

    assert result is True


@patch("fcm.core.kernel.run_make")
@patch("fcm.core.kernel.download_firecracker_config")
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

    result = configure_kernel(kernel_dir)

    assert result is False


@patch("fcm.core.kernel.run_make")
@patch("fcm.core.kernel.download_firecracker_config")
def test_configure_kernel_defconfig_also_fails(
    mock_download: MagicMock, mock_run_make: MagicMock, tmp_path: Path
):
    mock_download.return_value = False
    mock_run_make.return_value = (1, "", "defconfig error")

    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    result = configure_kernel(kernel_dir)

    assert result is False


@patch("fcm.core.kernel.run_make")
@patch("fcm.core.kernel.download_firecracker_config")
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

    with patch("fcm.core.kernel.subprocess.run") as mock_subprocess:
        mock_subprocess.return_value = MagicMock(returncode=0)
        result = configure_kernel(kernel_dir)

    assert result is False


@patch("fcm.core.kernel.download_firecracker_config")
@patch("fcm.core.kernel.subprocess.run")
def test_configure_kernel_missing_required_settings(
    mock_run: MagicMock, mock_download: MagicMock, tmp_path: Path
):
    mock_download.return_value = True
    mock_run.return_value = MagicMock(returncode=0)

    kernel_dir = tmp_path / "linux-src"
    scripts_dir = kernel_dir / "scripts"
    scripts_dir.mkdir(parents=True)
    (scripts_dir / "config").write_text("#!/bin/bash\necho config")

    config_path = kernel_dir / ".config"
    config_path.write_text("CONFIG_BTRFS_FS=y\n")

    result = configure_kernel(kernel_dir)

    assert result is False


def test_build_kernel_success(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()
    vmlinux = kernel_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF" + b"\x00" * 100)

    output_path = tmp_path / "out" / "vmlinux"

    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = ""
    mock_result.stderr = ""

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result):
        result = build_kernel(kernel_dir, output_path, jobs=2)

    assert result is True
    assert output_path.exists()


def test_build_kernel_failure(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_result = MagicMock()
    mock_result.returncode = 2
    mock_result.stdout = ""
    mock_result.stderr = "error: something broke\n"

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result):
        result = build_kernel(kernel_dir, output_path, jobs=1)

    assert result is False


def test_build_kernel_failure_with_error_lines(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_result = MagicMock()
    mock_result.returncode = 2
    mock_result.stdout = ""
    mock_result.stderr = "CC some/file.o\nerror: undefined reference to 'foo'\nLD vmlinux\n"

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result):
        result = build_kernel(kernel_dir, output_path, jobs=1)

    assert result is False


def test_build_kernel_vmlinux_not_found(tmp_path: Path):
    kernel_dir = tmp_path / "linux-src"
    kernel_dir.mkdir()

    output_path = tmp_path / "out" / "vmlinux"

    mock_result = MagicMock()
    mock_result.returncode = 0
    mock_result.stdout = ""
    mock_result.stderr = ""

    with patch("fcm.core.kernel.subprocess.run", return_value=mock_result):
        result = build_kernel(kernel_dir, output_path, jobs=2)

    assert result is False


def test_build_kernel_pipeline_cached(tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    output_path.write_bytes(b"cached-kernel")

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=tmp_path / "build",
    )

    assert result is True


def test_build_kernel_pipeline_download_fails(tmp_path: Path):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    with patch("fcm.core.kernel.download_kernel_source", return_value=False):
        result = build_kernel_pipeline(
            version="6.1.102",
            source_url="https://example.com/linux.tar.xz",
            output_path=output_path,
            build_dir=build_dir,
        )

    assert result is False


@patch("fcm.core.kernel.build_kernel")
@patch("fcm.core.kernel.configure_kernel")
@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_full_success(
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

    mock_download.return_value = True
    mock_extract.return_value = kernel_src_dir
    mock_configure.return_value = True
    mock_build.return_value = True

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    assert result is True
    mock_download.assert_called_once()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()


@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_extract_fails(
    mock_download: MagicMock, mock_extract: MagicMock, tmp_path: Path
):
    output_path = tmp_path / "vmlinux"
    build_dir = tmp_path / "build"
    build_dir.mkdir()

    tarball = build_dir / "linux-6.1.102.tar.xz"
    tarball.write_bytes(b"fake tarball")

    mock_download.return_value = True
    mock_extract.return_value = None

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    assert result is False


@patch("fcm.core.kernel.configure_kernel")
@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
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
    mock_configure.return_value = False

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    assert result is False


@patch("fcm.core.kernel.build_kernel")
@patch("fcm.core.kernel.configure_kernel")
@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
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
    mock_build.return_value = False

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    assert result is False


@patch("fcm.core.kernel.build_kernel")
@patch("fcm.core.kernel.configure_kernel")
@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
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

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
    )

    assert result is True
    mock_download.assert_not_called()
    mock_extract.assert_not_called()


@patch("fcm.core.kernel.build_kernel")
@patch("fcm.core.kernel.configure_kernel")
@patch("fcm.core.kernel.extract_kernel_tarball")
@patch("fcm.core.kernel.download_kernel_source")
def test_build_kernel_pipeline_cached_tarball_needs_extract(
    mock_download: MagicMock,
    mock_extract: MagicMock,
    mock_configure: MagicMock,
    mock_build: MagicMock,
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

    result = build_kernel_pipeline(
        version="6.1.102",
        source_url="https://example.com/linux.tar.xz",
        output_path=output_path,
        build_dir=build_dir,
        jobs=4,
    )

    assert result is True
    mock_download.assert_not_called()
    mock_extract.assert_called_once()
    mock_configure.assert_called_once()
    mock_build.assert_called_once()
