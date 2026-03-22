"""Tests for kernel download and build utilities."""

import tarfile
from pathlib import Path
from unittest.mock import patch, MagicMock
from urllib.error import URLError

from fcm.core.kernel import (
    download_kernel_source,
    extract_kernel_tarball,
    run_make,
    build_kernel,
    build_kernel_pipeline,
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
    # Create a real tar.xz with a linux-6.1/ directory inside
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
