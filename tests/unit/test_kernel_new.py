from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from mvmctl.core.kernel import (
    download_firecracker_kernel,
    fetch_kernel_sha256,
    get_default_kernel_path,
    list_kernels,
    parse_kernel_filename,
    save_kernel_metadata,
    set_default_kernel,
)
from mvmctl.exceptions import KernelError


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


def test_save_kernel_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    kernel_file = tmp_path / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)
    full_id = save_kernel_metadata(tmp_path, "vmlinux", version="6.1.9", kernel_type="official")
    import json

    assert len(full_id) == 64
    meta_file = tmp_path / "metadata.json"
    assert meta_file.exists()
    data = json.loads(meta_file.read_text())
    assert full_id in data["kernels"]
    entry = data["kernels"][full_id]
    assert entry["filename"] == "vmlinux"
    assert entry["full_hash"] == full_id
    assert entry["name"] == "vmlinux"
    assert entry["base_name"] == "vmlinux"
    assert entry["version"] == "6.1.9"
    assert entry["type"] == "official"
    assert "last_modified" in entry


def test_save_kernel_metadata_parses_filename(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    kernel_file = tmp_path / "vmlinux-fc-v1.15-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)
    full_id = save_kernel_metadata(tmp_path, "vmlinux-fc-v1.15-x86_64", kernel_type="firecracker")
    import json

    assert len(full_id) == 64
    meta_file = tmp_path / "metadata.json"
    data = json.loads(meta_file.read_text())
    assert full_id in data["kernels"]
    entry = data["kernels"][full_id]
    assert entry["filename"] == "vmlinux-fc-v1.15-x86_64"
    assert entry["full_hash"] == full_id
    assert entry["base_name"] == "vmlinux-fc"
    assert entry["version"] == "v1.15"
    assert entry["arch"] == "x86_64"
    assert entry["type"] == "firecracker"
    assert "last_modified" in entry


def test_list_kernels_empty(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    result = list_kernels(tmp_path)
    assert result == []


def test_list_kernels_with_file(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF" + b"\x00" * 100)
    import json as _json

    fake_id = "a" * 64
    (tmp_path / "metadata.json").write_text(
        _json.dumps(
            {
                "kernels": {
                    fake_id: {"filename": "vmlinux", "last_modified": "2026-01-01T00:00:00"}
                },
                "images": {},
            }
        )
    )
    result = list_kernels(tmp_path)
    assert len(result) == 1
    assert result[0]["id"] == fake_id[:6]
    assert result[0]["full_name"] == "vmlinux"
    assert "size" in result[0]


def test_list_kernels_with_metadata(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF" + b"\x00" * 100)
    save_kernel_metadata(tmp_path, "vmlinux", version="6.1.9", kernel_type="official")
    result = list_kernels(tmp_path)
    assert len(result) == 1
    assert result[0]["version"] == "6.1.9"
    assert result[0]["type"] == "official"


def test_list_kernels_skips_json_files(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    import json as _json

    (tmp_path / "metadata.json").write_text(
        _json.dumps(
            {"kernels": {"vmlinux": {"last_modified": "2026-01-01T00:00:00"}}, "images": {}}
        )
    )
    result = list_kernels(tmp_path)
    assert len(result) == 1


def test_set_default_kernel(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    set_default_kernel(tmp_path, "vmlinux")
    import json

    config_file = tmp_path / "config.json"
    assert config_file.exists()
    data = json.loads(config_file.read_text())
    assert data["defaults"]["kernel"] == "vmlinux"


def test_set_default_kernel_not_found(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    with pytest.raises(KernelError):
        set_default_kernel(tmp_path, "nonexistent")


def test_get_default_kernel_path_set(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    vmlinux = tmp_path / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    set_default_kernel(tmp_path, "vmlinux")
    result = get_default_kernel_path(tmp_path)
    assert result == vmlinux


def test_get_default_kernel_path_no_fallback(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    vmlinux = tmp_path / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    result = get_default_kernel_path(tmp_path)
    assert result is None


def test_get_default_kernel_path_none(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    result = get_default_kernel_path(tmp_path)
    assert result is None


def test_list_kernels_shows_default_marker(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    (tmp_path / "vmlinux").write_bytes(b"\x7fELF")
    import json as _json

    (tmp_path / "metadata.json").write_text(
        _json.dumps(
            {"kernels": {"vmlinux": {"last_modified": "2026-01-01T00:00:00"}}, "images": {}}
        )
    )
    set_default_kernel(tmp_path, "vmlinux")
    result = list_kernels(tmp_path)
    assert result[0]["is_default"] == "true"


@patch("mvmctl.core.kernel.urlopen")
def test_fetch_kernel_sha256_success(mock_urlopen: MagicMock):
    mock_resp = MagicMock()
    mock_resp.read.return_value = b"abcdef0123456789  linux-6.1.9.tar.xz\n"
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp

    result = fetch_kernel_sha256("6.1.9")
    assert result == "abcdef0123456789"


@patch("mvmctl.core.kernel.urlopen", side_effect=URLError("no network"))
def test_fetch_kernel_sha256_failure(mock_urlopen: MagicMock):
    result = fetch_kernel_sha256("6.1.9")
    assert result is None


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_success(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))
    xml_response = b"""<?xml version="1.0"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""
    mock_resp = MagicMock()
    mock_resp.read.return_value = xml_response
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_urlopen.return_value = mock_resp

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    result = download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)
    from mvmctl.core.kernel import load_kernel_spec

    firecracker_spec = load_kernel_spec("kernel-firecracker")
    assert result.name == f"{firecracker_spec.output_name}-6.1.9-amd64"
    assert result.exists()
    assert mock_dl.call_args.kwargs["expected_sha256"] is None
    assert mock_dl.call_args.kwargs["allow_missing_checksum"] is True
    assert mock_dl.call_args.kwargs["silent_missing_checksum"] is True


def test_load_kernel_spec_firecracker_has_templates():
    from mvmctl.core.kernel import load_kernel_spec

    spec = load_kernel_spec("kernel-firecracker")
    assert spec.name == "kernel-firecracker"
    assert spec.kernel_type == "firecracker"
    assert spec.source.startswith("https://")
    assert spec.list_url_template is not None
    assert "{ci_version}" in spec.list_url_template
    assert spec.config_url_template is not None
    assert ("{major_minor}" in spec.config_url_template) or (
        "{version}" in spec.config_url_template
    )


def test_resolve_kernel_spec_by_type_and_version():
    from mvmctl.core.kernel import resolve_kernel_spec

    spec = resolve_kernel_spec(kernel_type="firecracker", version="6.1")
    assert spec.kernel_type == "firecracker"
    assert spec.version == "6.1"


@patch("mvmctl.core.kernel.urlopen", side_effect=URLError("network error"))
def test_download_firecracker_kernel_list_failure(mock_urlopen: MagicMock, tmp_path: Path):
    with pytest.raises(KernelError):
        download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)


@patch("mvmctl.core.kernel.urlopen")
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


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_requires_checksum_when_sha256_url_set(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    from mvmctl.models.kernel import KernelSpec

    spec_with_sha256_url = KernelSpec(
        name="kernel-firecracker-test",
        kernel_type="firecracker",
        version="6.1",
        source="https://example.com/firecracker-ci/{ci_version}/{arch}/vmlinux-{kernel_version}",
        output_name="vmlinux-fc-test",
        build_dir=str(tmp_path / "build"),
        list_url_template="http://example.com/?prefix=firecracker-ci/{ci_version}/{arch}/vmlinux-{version}",
        sha256=None,
        sha256_url="https://example.com/sha256sums",
    )

    xml_response = b"""<?xml version="1.0"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""

    list_resp = MagicMock()
    list_resp.read.return_value = xml_response
    list_resp.__enter__ = lambda s: s
    list_resp.__exit__ = MagicMock(return_value=False)

    mock_urlopen.side_effect = [list_resp, URLError("missing sidecar")]

    with pytest.raises(KernelError, match="Checksum required"):
        download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path, kernel_spec=spec_with_sha256_url)

    mock_dl.assert_not_called()


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_supports_version_placeholder_in_source(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))

    xml_response = b"""<?xml version="1.0"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""

    list_resp = MagicMock()
    list_resp.read.return_value = xml_response
    list_resp.__enter__ = lambda s: s
    list_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        b"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  vmlinux"
    )
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    mock_urlopen.side_effect = [list_resp, sha_resp]

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    result = download_firecracker_kernel("1.12", "amd64", kernels_dir=tmp_path)
    assert result.exists()

    from mvmctl.core.kernel import load_kernel_spec

    firecracker_spec = load_kernel_spec("kernel-firecracker")
    called_download_url = mock_dl.call_args.args[0]
    chosen_key = "firecracker-ci/1.12/amd64/vmlinux-6.1.9"
    expected_download_url = f"{firecracker_spec.source.rstrip('/')}/{chosen_key}"
    assert called_download_url == expected_download_url


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_uses_templated_sha256_url(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    monkeypatch.setenv("MVM_CACHE_DIR", str(tmp_path))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(tmp_path))

    xml_response = b"""<?xml version="1.0"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""

    list_resp = MagicMock()
    list_resp.read.return_value = xml_response
    list_resp.__enter__ = lambda s: s
    list_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        b"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  vmlinux"
    )
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    mock_urlopen.side_effect = [list_resp, sha_resp]

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    from mvmctl.core.kernel import load_kernel_spec

    firecracker_spec = load_kernel_spec("kernel-firecracker")
    firecracker_spec.sha256_url = "https://example.com/{ci_version}/{arch}/vmlinux-{version}.sha256"

    result = download_firecracker_kernel(
        "1.12", "amd64", kernels_dir=tmp_path, kernel_spec=firecracker_spec
    )

    assert result.exists()
    sha_request = mock_urlopen.call_args_list[1].args[0]
    assert sha_request.full_url == "https://example.com/1.12/amd64/vmlinux-6.1.sha256"
