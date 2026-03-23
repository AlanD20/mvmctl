"""Tests for core/binary_manager.py."""

import hashlib
import io
import json
import os
import tarfile
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock, patch
from urllib.error import URLError

import pytest

from fcm.core.binary_manager import (
    _normalize_version,
    fetch_binary,
    get_bin_dir,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from fcm.exceptions import AssetNotFoundError, BinaryError


# ---------------------------------------------------------------------------
# _normalize_version
# ---------------------------------------------------------------------------


def test_normalize_version_strips_v_prefix():
    assert _normalize_version("v1.0.0") == "1.0.0"


def test_normalize_version_no_prefix_unchanged():
    assert _normalize_version("1.0.0") == "1.0.0"


def test_normalize_version_empty_string():
    assert _normalize_version("") == ""


# ---------------------------------------------------------------------------
# get_bin_dir
# ---------------------------------------------------------------------------


def test_get_bin_dir_returns_path_under_cache(tmp_path: Path):
    with patch.dict(os.environ, {"FCM_CACHE_DIR": str(tmp_path)}):
        result = get_bin_dir()
    assert result == tmp_path / "bin"


# ---------------------------------------------------------------------------
# list_local_versions
# ---------------------------------------------------------------------------


def test_list_local_versions_empty_dir(tmp_path: Path):
    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_paired_binaries(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    result = list_local_versions(tmp_path)
    assert len(result) == 1
    assert result[0].version == "1.0.0"
    assert result[0].firecracker_path == tmp_path / "firecracker-v1.0.0"
    assert result[0].jailer_path == tmp_path / "jailer-v1.0.0"
    assert result[0].is_active is False


def test_list_local_versions_unpaired_skipped(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    # jailer-v1.0.0 intentionally missing
    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_active_symlink(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    result = list_local_versions(tmp_path)
    assert len(result) == 1
    assert result[0].is_active is True


def test_list_local_versions_active_symlink_different_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker-v2.0.0").touch()
    (tmp_path / "jailer-v2.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v2.0.0")
    result = list_local_versions(tmp_path)
    assert len(result) == 2
    active = [v for v in result if v.is_active]
    inactive = [v for v in result if not v.is_active]
    assert len(active) == 1
    assert active[0].version == "2.0.0"
    assert len(inactive) == 1
    assert inactive[0].version == "1.0.0"


def test_list_local_versions_multiple_sorted_reverse(tmp_path: Path):
    for ver in ("1.0.0", "1.1.0", "2.0.0"):
        (tmp_path / f"firecracker-v{ver}").touch()
        (tmp_path / f"jailer-v{ver}").touch()
    result = list_local_versions(tmp_path)
    versions = [v.version for v in result]
    assert versions == ["2.0.0", "1.1.0", "1.0.0"]


def test_list_local_versions_ignores_directories(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").mkdir()
    (tmp_path / "jailer-v1.0.0").touch()
    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_ignores_symlinks(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    result = list_local_versions(tmp_path)
    # Symlinks are not counted as extra versions
    assert len(result) == 1


# ---------------------------------------------------------------------------
# list_remote_versions
# ---------------------------------------------------------------------------


def _mock_github_response(releases: list[dict[str, Any]]) -> MagicMock:
    body = json.dumps(releases).encode()
    mock_resp = MagicMock()
    mock_resp.read.return_value = body
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    return mock_resp


def test_list_remote_versions_success():
    releases = [
        {"tag_name": "v1.5.0"},
        {"tag_name": "v1.4.0"},
    ]
    mock_resp = _mock_github_response(releases)
    with patch("fcm.core.binary_manager.urlopen", return_value=mock_resp):
        result = list_remote_versions(limit=5)
    assert result == ["1.5.0", "1.4.0"]


def test_list_remote_versions_strips_v_prefix():
    releases = [{"tag_name": "v2.0.0"}]
    mock_resp = _mock_github_response(releases)
    with patch("fcm.core.binary_manager.urlopen", return_value=mock_resp):
        result = list_remote_versions()
    assert result == ["2.0.0"]


def test_list_remote_versions_skips_non_string_tags():
    releases = [
        {"tag_name": "v1.0.0"},
        {"tag_name": None},
        {"other_key": "v2.0.0"},
    ]
    mock_resp = _mock_github_response(releases)
    with patch("fcm.core.binary_manager.urlopen", return_value=mock_resp):
        result = list_remote_versions()
    assert result == ["1.0.0"]


def test_list_remote_versions_network_error():
    with patch("fcm.core.binary_manager.urlopen", side_effect=URLError("timeout")):
        with pytest.raises(BinaryError, match="Failed to fetch releases"):
            list_remote_versions()


def test_list_remote_versions_os_error():
    with patch("fcm.core.binary_manager.urlopen", side_effect=OSError("connection reset")):
        with pytest.raises(BinaryError, match="Failed to fetch releases"):
            list_remote_versions()


# ---------------------------------------------------------------------------
# fetch_binary
# ---------------------------------------------------------------------------


def _make_tarball(tmp_path: Path, version: str) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for name in (f"firecracker-v{version}-x86_64", f"jailer-v{version}-x86_64"):
            content = b"#!/bin/sh\necho fake\n"
            info = tarfile.TarInfo(name=f"release-v{version}-x86_64/{name}")
            info.size = len(content)
            tar.addfile(info, io.BytesIO(content))
    return buf.getvalue()


def test_fetch_binary_already_exists(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    result = fetch_binary("1.0.0", bin_dir=tmp_path)
    assert result.version == "1.0.0"
    assert result.firecracker_path == tmp_path / "firecracker-v1.0.0"
    assert result.jailer_path == tmp_path / "jailer-v1.0.0"


def test_fetch_binary_already_exists_normalizes_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    result = fetch_binary("v1.0.0", bin_dir=tmp_path)
    assert result.version == "1.0.0"


def test_fetch_binary_already_exists_with_active_symlink(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    result = fetch_binary("1.0.0", bin_dir=tmp_path)
    assert result.is_active is True


def test_fetch_binary_downloads_and_extracts(tmp_path: Path):
    tarball_data = _make_tarball(tmp_path, "1.5.0")
    mock_resp = MagicMock()
    mock_resp.read.side_effect = [tarball_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        hashlib.sha256(tarball_data).hexdigest() + "  firecracker-v1.5.0-x86_64.tgz\n"
    ).encode()
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    with patch("fcm.core.binary_manager.urlopen", side_effect=[mock_resp, sha_resp]):
        result = fetch_binary("1.5.0", bin_dir=tmp_path)

    assert result.version == "1.5.0"
    assert result.firecracker_path.exists()
    assert result.jailer_path.exists()
    assert os.access(result.firecracker_path, os.X_OK)
    assert os.access(result.jailer_path, os.X_OK)
    # Tarball should be cleaned up
    assert not (tmp_path / "firecracker-v1.5.0-x86_64.tgz").exists()


def test_fetch_binary_download_failure_cleans_up(tmp_path: Path):
    with patch("fcm.core.binary_manager.urlopen", side_effect=URLError("network error")):
        with pytest.raises(BinaryError, match="Failed to download"):
            fetch_binary("1.5.0", bin_dir=tmp_path)
    # No partial tgz left behind
    assert not (tmp_path / "firecracker-v1.5.0-x86_64.tgz").exists()


def test_fetch_binary_missing_binaries_in_archive(tmp_path: Path):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        content = b"fake"
        info = tarfile.TarInfo(name="release/firecracker-v1.5.0-x86_64")
        info.size = len(content)
        tar.addfile(info, io.BytesIO(content))
    tarball_data = buf.getvalue()

    mock_resp = MagicMock()
    mock_resp.read.side_effect = [tarball_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        hashlib.sha256(tarball_data).hexdigest() + "  file.tgz\n"
    ).encode()
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    with patch("fcm.core.binary_manager.urlopen", side_effect=[mock_resp, sha_resp]):
        with pytest.raises(BinaryError, match="missing expected binaries"):
            fetch_binary("1.5.0", bin_dir=tmp_path)


def test_fetch_binary_corrupt_archive(tmp_path: Path):
    corrupt_data = b"not a valid tarball"
    mock_resp = MagicMock()
    mock_resp.read.side_effect = [corrupt_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        hashlib.sha256(corrupt_data).hexdigest() + "  file.tgz\n"
    ).encode()
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    with patch("fcm.core.binary_manager.urlopen", side_effect=[mock_resp, sha_resp]):
        with pytest.raises(BinaryError):
            fetch_binary("1.5.0", bin_dir=tmp_path)
    # Partial files cleaned up
    assert not (tmp_path / "firecracker-v1.5.0").exists()
    assert not (tmp_path / "jailer-v1.5.0").exists()


# ---------------------------------------------------------------------------
# set_active_version
# ---------------------------------------------------------------------------


def test_set_active_version_creates_symlinks(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    set_active_version("1.0.0", bin_dir=tmp_path)
    fc_link = tmp_path / "firecracker"
    jl_link = tmp_path / "jailer"
    assert fc_link.is_symlink()
    assert jl_link.is_symlink()
    assert os.readlink(fc_link) == "firecracker-v1.0.0"
    assert os.readlink(jl_link) == "jailer-v1.0.0"


def test_set_active_version_normalizes_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    set_active_version("v1.0.0", bin_dir=tmp_path)
    assert (tmp_path / "firecracker").is_symlink()


def test_set_active_version_replaces_existing_symlinks(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker-v2.0.0").touch()
    (tmp_path / "jailer-v2.0.0").touch()
    set_active_version("1.0.0", bin_dir=tmp_path)
    set_active_version("2.0.0", bin_dir=tmp_path)
    assert os.readlink(tmp_path / "firecracker") == "firecracker-v2.0.0"
    assert os.readlink(tmp_path / "jailer") == "jailer-v2.0.0"


def test_set_active_version_binaries_missing(tmp_path: Path):
    with pytest.raises(AssetNotFoundError, match="not downloaded"):
        set_active_version("1.0.0", bin_dir=tmp_path)


def test_set_active_version_partial_binaries_missing(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    # jailer-v1.0.0 missing
    with pytest.raises(AssetNotFoundError, match="not downloaded"):
        set_active_version("1.0.0", bin_dir=tmp_path)


# ---------------------------------------------------------------------------
# remove_version
# ---------------------------------------------------------------------------


def test_remove_version_deletes_files(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()
    assert not (tmp_path / "jailer-v1.0.0").exists()


def test_remove_version_normalizes_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    remove_version("v1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()


def test_remove_version_not_found(tmp_path: Path):
    with pytest.raises(AssetNotFoundError, match="not found locally"):
        remove_version("9.9.9", bin_dir=tmp_path)


def test_remove_active_version_removes_symlinks(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker").exists()
    assert not (tmp_path / "jailer").exists()
    assert not (tmp_path / "firecracker-v1.0.0").exists()
    assert not (tmp_path / "jailer-v1.0.0").exists()


def test_remove_version_leaves_other_symlinks(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker-v2.0.0").touch()
    (tmp_path / "jailer-v2.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v2.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v2.0.0")
    remove_version("1.0.0", bin_dir=tmp_path)
    # v2 symlinks still intact
    assert (tmp_path / "firecracker").is_symlink()
    assert (tmp_path / "jailer").is_symlink()
    assert os.readlink(tmp_path / "firecracker") == "firecracker-v2.0.0"


def test_remove_version_partial_files(tmp_path: Path):
    # Only firecracker exists, no jailer — still should remove what's there
    (tmp_path / "firecracker-v1.0.0").touch()
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()


# ---------------------------------------------------------------------------
# _extract_member — None reader
# ---------------------------------------------------------------------------


def test_extract_member_none_reader():
    """extractfile returns None for directories/links — should raise BinaryError."""
    from fcm.core.binary_manager import _extract_member

    mock_tar = MagicMock(spec=tarfile.TarFile)
    mock_member = MagicMock(spec=tarfile.TarInfo)
    mock_member.name = "some-dir/"
    mock_tar.extractfile.return_value = None

    with pytest.raises(BinaryError, match="Cannot read"):
        _extract_member(mock_tar, mock_member, Path("/tmp/dest"))


# ---------------------------------------------------------------------------
# _resolve_bin_dir
# ---------------------------------------------------------------------------


def test_resolve_bin_dir_with_none_uses_default(tmp_path: Path):
    """When bin_dir is None, _resolve_bin_dir uses get_bin_dir()."""
    from fcm.core.binary_manager import _resolve_bin_dir

    with patch("fcm.core.binary_manager.get_bin_dir", return_value=tmp_path / "bins"):
        result = _resolve_bin_dir(None)
    assert result == tmp_path / "bins"
    assert result.exists()


def test_resolve_bin_dir_with_explicit_path(tmp_path: Path):
    """When bin_dir is provided, use it directly and ensure it exists."""
    from fcm.core.binary_manager import _resolve_bin_dir

    custom = tmp_path / "custom" / "bin"
    result = _resolve_bin_dir(custom)
    assert result == custom
    assert result.exists()


# ---------------------------------------------------------------------------
# _active_target
# ---------------------------------------------------------------------------


def test_active_target_symlink_exists(tmp_path: Path):
    """_active_target returns the symlink target when it's a symlink."""
    from fcm.core.binary_manager import _active_target

    (tmp_path / "firecracker-v1.0.0").touch()
    link = tmp_path / "firecracker"
    link.symlink_to("firecracker-v1.0.0")
    assert _active_target(link) == "firecracker-v1.0.0"


def test_active_target_not_a_symlink(tmp_path: Path):
    """_active_target returns None when the path is not a symlink."""
    from fcm.core.binary_manager import _active_target

    regular_file = tmp_path / "firecracker"
    regular_file.touch()
    assert _active_target(regular_file) is None


def test_active_target_path_does_not_exist(tmp_path: Path):
    """_active_target returns None when the path doesn't exist."""
    from fcm.core.binary_manager import _active_target

    assert _active_target(tmp_path / "nonexistent") is None


# ---------------------------------------------------------------------------
# S-H3: SHA-256 verification tests
# ---------------------------------------------------------------------------


def test_fetch_binary_sha256_mismatch(tmp_path: Path):
    """SHA-256 mismatch with sidecar should raise BinaryError."""
    tarball_data = _make_tarball(tmp_path, "1.5.0")
    mock_resp = MagicMock()
    mock_resp.read.side_effect = [tarball_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    sha_resp = MagicMock()
    sha_resp.read.return_value = (
        b"0000000000000000000000000000000000000000000000000000000000000000  file.tgz\n"
    )
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)

    with patch("fcm.core.binary_manager.urlopen", side_effect=[mock_resp, sha_resp]):
        with pytest.raises(BinaryError, match="SHA-256 mismatch"):
            fetch_binary("1.5.0", bin_dir=tmp_path)


def test_fetch_binary_sha256_sidecar_unavailable(tmp_path: Path):
    """When SHA sidecar is unavailable, fetch should continue with a warning."""
    tarball_data = _make_tarball(tmp_path, "1.6.0")
    mock_resp = MagicMock()
    mock_resp.read.side_effect = [tarball_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    with patch("fcm.core.binary_manager.urlopen", side_effect=[mock_resp, URLError("404")]):
        result = fetch_binary("1.6.0", bin_dir=tmp_path)

    assert result.version == "1.6.0"
    assert result.firecracker_path.exists()
    assert result.jailer_path.exists()
