from pathlib import Path
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from mvmctl.core.kernel import (
    download_firecracker_kernel,
    get_default_kernel_path,
    list_kernels,
    parse_kernel_filename,
    save_kernel_metadata,
    set_default_kernel,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import Kernel
from mvmctl.exceptions import KernelError
from mvmctl.utils.fs import get_cache_dir


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
    """Test saving kernel metadata to SQLite database."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)

    full_id = save_kernel_metadata(kernels_dir, "vmlinux", version="6.1.9", kernel_type="official")

    assert len(full_id) == 64

    # Verify in SQLite database
    db = MVMDatabase()
    kernel = db.get_kernel(full_id)
    assert kernel is not None
    assert kernel.name == "vmlinux"
    assert kernel.base_name == "vmlinux"
    assert kernel.version == "6.1.9"
    assert kernel.type == "official"
    assert kernel.path == "vmlinux"


def test_save_kernel_metadata_parses_filename(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """Test saving kernel metadata with filename parsing."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    kernel_file = kernels_dir / "vmlinux-fc-v1.15-x86_64"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)

    full_id = save_kernel_metadata(
        kernels_dir, "vmlinux-fc-v1.15-x86_64", kernel_type="firecracker"
    )

    assert len(full_id) == 64

    # Verify in SQLite database
    db = MVMDatabase()
    kernel = db.get_kernel(full_id)
    assert kernel is not None
    assert kernel.name == "vmlinux-fc-v1.15-x86_64"
    assert kernel.base_name == "vmlinux-fc"
    assert kernel.version == "v1.15"
    assert kernel.arch == "x86_64"
    assert kernel.type == "firecracker"


def test_list_kernels_empty():
    """Test listing kernels when directory is empty."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    result = list_kernels(kernels_dir)
    assert result == []


def test_list_kernels_with_file():
    """Test listing kernels with a kernel file present."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)

    # Insert kernel into database directly
    db = MVMDatabase()
    fake_id = "a" * 16
    from datetime import datetime, timezone

    kernel = Kernel(
        id=fake_id,
        name="vmlinux",
        base_name="vmlinux",
        version="-",
        arch="-",
        type="official",
        path="vmlinux",
        is_default=False,
        created_at=datetime.now(timezone.utc).isoformat(),
        updated_at=datetime.now(timezone.utc).isoformat(),
    )
    db.upsert_kernel(kernel)

    result = list_kernels(kernels_dir)
    assert len(result) == 1
    assert result[0]["id"] == fake_id
    assert result[0]["full_name"] == "vmlinux"
    assert "size" in result[0]


def test_list_kernels_with_metadata():
    """Test listing kernels with metadata from database."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)

    # Save metadata via the function
    save_kernel_metadata(kernels_dir, "vmlinux", version="6.1.9", kernel_type="official")

    result = list_kernels(kernels_dir)
    assert len(result) == 1
    assert result[0]["version"] == "6.1.9"
    assert result[0]["type"] == "official"


def test_list_kernels_shows_orphaned_entries():
    """Test that orphaned kernel entries (file missing) are still shown for CLI X mark."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF" + b"\x00" * 100)

    # Save metadata
    save_kernel_metadata(kernels_dir, "vmlinux", version="6.1.9", kernel_type="official")

    # List should find the kernel
    result = list_kernels(kernels_dir)
    assert len(result) == 1

    # Remove the file
    kernel_file.unlink()

    # List should still show the kernel (orphaned entry shown with X mark in CLI)
    result = list_kernels(kernels_dir)
    assert len(result) == 1
    assert result[0]["size"] == "0.0 MiB"  # Size is 0 when file missing


def test_set_default_kernel():
    """Test setting a kernel as default."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF")

    # Save metadata
    kernel_id = save_kernel_metadata(
        kernels_dir, "vmlinux", version="6.1.9", kernel_type="official"
    )

    # Set as default
    set_default_kernel(kernels_dir, "vmlinux")

    # Verify in database
    db = MVMDatabase()
    kernel = db.get_kernel(kernel_id)
    assert kernel is not None
    assert kernel.is_default


def test_set_default_kernel_not_found():
    """Test setting nonexistent kernel as default raises error."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    with pytest.raises(KernelError):
        set_default_kernel(kernels_dir, "nonexistent")


def test_get_default_kernel_path_set():
    """Test getting default kernel path when set."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    vmlinux = kernels_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")

    # Save metadata and set as default
    save_kernel_metadata(kernels_dir, "vmlinux", version="6.1.9", kernel_type="official")
    set_default_kernel(kernels_dir, "vmlinux")

    result = get_default_kernel_path(kernels_dir)
    assert result == vmlinux


def test_get_default_kernel_path_no_fallback():
    """Test getting default kernel returns None when no default set."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file without setting default
    vmlinux = kernels_dir / "vmlinux"
    vmlinux.write_bytes(b"\x7fELF")
    save_kernel_metadata(kernels_dir, "vmlinux", version="6.1.9", kernel_type="official")

    result = get_default_kernel_path(kernels_dir)
    assert result is None


def test_get_default_kernel_path_none():
    """Test getting default kernel when no kernels exist."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    result = get_default_kernel_path(kernels_dir)
    assert result is None


def test_list_kernels_shows_default_marker():
    """Test that default kernel is marked in list output."""
    cache_dir = get_cache_dir()
    kernels_dir = cache_dir / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    # Create kernel file
    kernel_file = kernels_dir / "vmlinux"
    kernel_file.write_bytes(b"\x7fELF")

    # Insert kernel into database and set as default
    db = MVMDatabase()
    kernel_id = "a" * 16
    from datetime import datetime, timezone

    kernel = Kernel(
        id=kernel_id,
        name="vmlinux",
        base_name="vmlinux",
        version="-",
        arch="-",
        type="official",
        path=str(kernel_file.relative_to(cache_dir)),
        is_default=True,
        created_at=datetime.now(timezone.utc).isoformat(),
        updated_at=datetime.now(timezone.utc).isoformat(),
    )
    db.upsert_kernel(kernel)
    db.set_default_kernel(kernel_id)

    result = list_kernels(kernels_dir)
    assert result[0]["is_default"] == "true"


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
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

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    kernels_dir = tmp_path / "cache" / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    result = download_firecracker_kernel("1.12", "amd64", kernels_dir=kernels_dir)
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
        download_firecracker_kernel(
            "1.12", "amd64", kernels_dir=tmp_path, kernel_spec=spec_with_sha256_url
        )

    mock_dl.assert_not_called()


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_supports_version_placeholder_in_source(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
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

    kernels_dir = tmp_path / "cache" / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    result = download_firecracker_kernel("1.12", "amd64", kernels_dir=kernels_dir)
    assert result.exists()

    from mvmctl.core.kernel import load_kernel_spec

    firecracker_spec = load_kernel_spec("kernel-firecracker")
    called_download_url = mock_dl.call_args.args[0]
    chosen_key = "firecracker-ci/1.12/amd64/vmlinux-6.1.9"
    expected_download_url = f"{firecracker_spec.source.rstrip('/')}/{chosen_key}"
    assert called_download_url == expected_download_url


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_output_name_overrides_base_only(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    xml_response = b"""<?xml version=\"1.0\"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""

    list_resp = MagicMock()
    list_resp.read.return_value = xml_response
    list_resp.__enter__ = lambda s: s
    list_resp.__exit__ = MagicMock(return_value=False)

    mock_urlopen.side_effect = [list_resp]

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    kernels_dir = tmp_path / "cache" / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    result = download_firecracker_kernel(
        "1.12",
        "amd64",
        kernels_dir=kernels_dir,
        output_name="custom-base",
    )

    assert result.name == "custom-base-6.1.9-amd64"


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_output_path_is_explicit(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
    xml_response = b"""<?xml version=\"1.0\"?>
<ListBucketResult>
<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>
</ListBucketResult>"""

    list_resp = MagicMock()
    list_resp.read.return_value = xml_response
    list_resp.__enter__ = lambda s: s
    list_resp.__exit__ = MagicMock(return_value=False)

    mock_urlopen.side_effect = [list_resp]

    def fake_download(url, dest, **kw):
        dest.write_bytes(b"\x7fELF")
        return True

    mock_dl.side_effect = fake_download

    kernels_dir = tmp_path / "cache" / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    explicit_out = kernels_dir / "my-explicit-kernel"
    result = download_firecracker_kernel(
        "1.12",
        "amd64",
        kernels_dir=kernels_dir,
        output_name="ignored-base",
        output_path=explicit_out,
    )

    assert result == explicit_out


@patch("mvmctl.core.kernel.download_file")
@patch("mvmctl.core.kernel.urlopen")
def test_download_firecracker_kernel_uses_templated_sha256_url(
    mock_urlopen: MagicMock, mock_dl: MagicMock, tmp_path: Path
):
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

    kernels_dir = tmp_path / "cache" / "kernels"
    kernels_dir.mkdir(parents=True, exist_ok=True)

    from mvmctl.core.kernel import load_kernel_spec

    firecracker_spec = load_kernel_spec("kernel-firecracker")
    firecracker_spec.sha256_url = "https://example.com/{ci_version}/{arch}/vmlinux-{version}.sha256"

    result = download_firecracker_kernel(
        "1.12", "amd64", kernels_dir=kernels_dir, kernel_spec=firecracker_spec
    )

    assert result.exists()
    sha_request = mock_urlopen.call_args_list[1].args[0]
    assert sha_request.full_url == "https://example.com/1.12/amd64/vmlinux-6.1.sha256"
