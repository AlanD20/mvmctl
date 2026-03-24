from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from fcm.core.kernel import (
    download_firecracker_kernel,
    fetch_kernel_sha256,
    get_default_kernel_path,
    list_kernels,
    parse_kernel_filename,
    save_kernel_metadata,
    set_default_kernel,
)
from fcm.exceptions import KernelError


def test_parse_kernel_filename_fc_with_v_prefix():
    result = parse_kernel_filename("vmlinux-fc-v1.15-x86_64")
    assert result.base_name == "vmlinux-fc"
    assert result.version == "v1.15"
    assert result.arch == "x86_64"


def test_parse_kernel_filename_fc_without_v_prefix():
    result = parse_kernel_filename("vmlinux-fc-1.15-arm64")
    assert result.base_name == "vmlinux-fc"
    assert result.version == "1.15"
    assert result.arch == "arm64"


def test_parse_kernel_filename_official():
    result = parse_kernel_filename("vmlinux-6.1.102")
    assert result.base_name == "vmlinux"
    assert result.version == "6.1.102"
    assert result.arch == "-"


def test_parse_kernel_filename_plain():
    result = parse_kernel_filename("vmlinux")
    assert result.base_name == "vmlinux"
    assert result.version == "-"
    assert result.arch == "-"


def test_parse_kernel_filename_with_amd64():
    result = parse_kernel_filename("vmlinux-fc-1.12-amd64")
    assert result.base_name == "vmlinux-fc"
    assert result.version == "1.12"
    assert result.arch == "amd64"


def test_parse_kernel_filename_with_aarch64():
    result = parse_kernel_filename("vmlinux-6.1-aarch64")
    assert result.base_name == "vmlinux"
    assert result.version == "6.1"
    assert result.arch == "aarch64"


def test_save_kernel_metadata(tmp_path: Path):
    kernel_file = tmp_path / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)
    save_kernel_metadata(tmp_path, "vmlinux", version="6.1.9", kernel_type="official")
    meta_file = tmp_path / "vmlinux.json"
    assert meta_file.exists()
    import json

    data = json.loads(meta_file.read_text())
    assert data["name"] == "vmlinux"
    assert data["base_name"] == "vmlinux"
    assert data["version"] == "6.1.9"
    assert data["type"] == "official"
    assert "last_modified" in data


def test_save_kernel_metadata_parses_filename(tmp_path: Path):
    kernel_file = tmp_path / "vmlinux-fc-v1.15-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)
    save_kernel_metadata(tmp_path, "vmlinux-fc-v1.15-x86_64", kernel_type="firecracker")
    meta_file = tmp_path / "vmlinux-fc-v1.15-x86_64.json"
    assert meta_file.exists()
    import json

    data = json.loads(meta_file.read_text())
    assert data["name"] == "vmlinux-fc-v1.15-x86_64"
    assert data["base_name"] == "vmlinux-fc"
    assert data["version"] == "v1.15"
    assert data["arch"] == "x86_64"
    assert data["type"] == "firecracker"
    assert "last_modified" in data


def test_list_kernels_empty(tmp_path: Path):
    result = list_kernels(tmp_path)
    assert result == []


def test_list_kernels_with_file(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF" + b"\x00" * 100)
    result = list_kernels(tmp_path)
    assert len(result) == 1
    assert result[0]["name"] == "vmlinux"
    assert "size" in result[0]


def test_list_kernels_with_metadata(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF" + b"\x00" * 100)
    save_kernel_metadata(tmp_path, "vmlinux", version="6.1.9", kernel_type="official")
    result = list_kernels(tmp_path)
    assert len(result) == 1
    assert result[0]["version"] == "6.1.9"
    assert result[0]["type"] == "official"


def test_list_kernels_skips_json_files(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    (tmp_path / "vmlinux.json").write_text('{"name": "vmlinux"}')
    result = list_kernels(tmp_path)
    assert len(result) == 1


def test_set_default_kernel(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    set_default_kernel(tmp_path, "vmlinux")
    default_file = tmp_path / "default.json"
    assert default_file.exists()


def test_set_default_kernel_not_found(tmp_path: Path):
    with pytest.raises(KernelError):
        set_default_kernel(tmp_path, "nonexistent")


def test_get_default_kernel_path_set(tmp_path: Path):
    vmlinux = tmp_path / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    set_default_kernel(tmp_path, "vmlinux")
    result = get_default_kernel_path(tmp_path)
    assert result == vmlinux


def test_get_default_kernel_path_fallback(tmp_path: Path):
    vmlinux = tmp_path / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    result = get_default_kernel_path(tmp_path)
    assert result == vmlinux


def test_get_default_kernel_path_none(tmp_path: Path):
    result = get_default_kernel_path(tmp_path)
    assert result is None


def test_list_kernels_shows_default_marker(tmp_path: Path):
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    set_default_kernel(tmp_path, "vmlinux")
    result = list_kernels(tmp_path)
    assert result[0]["is_default"] == "true"


@patch("fcm.core.kernel.urlopen")
def test_fetch_kernel_sha256_success(mock_urlopen: MagicMock):
    mock_resp = MagicMock()
    mock_resp.read.return_value = b"abcdef0123456789  linux-6.1.9.tar.xz\n"
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp

    result = fetch_kernel_sha256("6.1.9")
    assert result == "abcdef0123456789"


@patch("fcm.core.kernel.urlopen", side_effect=URLError("no network"))
def test_fetch_kernel_sha256_failure(mock_urlopen: MagicMock):
    result = fetch_kernel_sha256("6.1.9")
    assert result is None


@patch("fcm.core.kernel.download_file")
@patch("fcm.core.kernel.urlopen")
def test_download_firecracker_kernel_success(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    xml_response = b"""<?xml version="1.0"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""
    mock_resp = MagicMock()
    mock_resp.read.return_value = xml_response
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp

    kernel_file = tmp_path / "vmlinux-fc-1.12-amd64"

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    result = download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)
    assert result.name.startswith("vmlinux")
    assert result.exists()


@patch("fcm.core.kernel.urlopen", side_effect=URLError("network error"))
def test_download_firecracker_kernel_list_failure(mock_urlopen: MagicMock, tmp_path: Path):
    with pytest.raises(KernelError):
        download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)


@patch("fcm.core.kernel.urlopen")
def test_download_firecracker_kernel_no_keys(mock_urlopen: MagicMock, tmp_path: Path):
    mock_resp = MagicMock()
    mock_resp.read.return_value = (
        b"<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>"
    )
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp

    with pytest.raises(KernelError):
        download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)
