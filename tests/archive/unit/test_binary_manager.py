"""Tests for core/binary_manager.py."""

import hashlib
import io
import json
import os
import tarfile
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock
from urllib.error import URLError

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.binary_manager import (
    _active_target,
    _normalize_version,
    fetch_binary,
    get_binary_path,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from mvmctl.exceptions import AssetNotFoundError, BinaryError

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
# list_local_versions — filesystem scanning (core layer)
# ---------------------------------------------------------------------------


def test_list_local_versions_empty_directory(tmp_path: Path) -> None:
    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_paired_binaries(tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    result = list_local_versions(tmp_path)
    assert len(result) == 1
    assert result[0].version == "1.0.0"
    assert result[0].firecracker_path == fc_file
    assert result[0].jailer_path == jl_file
    assert result[0].is_active is False


def test_list_local_versions_no_jailer_match_skipped(tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    fc_file.touch()

    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_active_via_symlink(tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    # Create active symlink
    symlink = tmp_path / "firecracker"
    symlink.symlink_to("firecracker-v1.0.0")

    result = list_local_versions(tmp_path)
    assert len(result) == 1
    assert result[0].is_active is True


def test_list_local_versions_inactive_when_no_symlink_match(tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    # Create symlink pointing to different version
    symlink = tmp_path / "firecracker"
    symlink.symlink_to("firecracker-v2.0.0")

    result = list_local_versions(tmp_path)
    assert len(result) == 1
    assert result[0].is_active is False


def test_list_local_versions_multiple_versions_sorted(tmp_path: Path) -> None:
    versions = ["1.0.0", "1.1.0", "2.0.0"]
    for ver in versions:
        (tmp_path / f"firecracker-v{ver}").touch()
        (tmp_path / f"jailer-v{ver}").touch()

    result = list_local_versions(tmp_path)
    assert [r.version for r in result] == ["2.0.0", "1.1.0", "1.0.0"]


def test_list_local_versions_ignores_directories(tmp_path: Path) -> None:
    (tmp_path / "firecracker-v1.0.0").mkdir()
    (tmp_path / "jailer-v1.0.0").touch()
    result = list_local_versions(tmp_path)
    assert result == []


def test_list_local_versions_ignores_symlinks(tmp_path: Path) -> None:
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    result = list_local_versions(tmp_path)
    # Should still find the pair, but not count symlinks as versions
    assert len(result) == 1


def test_list_local_versions_skips_missing_files(tmp_path: Path) -> None:
    # Don't create any files
    result = list_local_versions(tmp_path)
    assert result == []


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


def test_list_remote_versions_success(mocker: MockerFixture):
    releases = [
        {"tag_name": "v1.5.0"},
        {"tag_name": "v1.4.0"},
    ]
    mock_resp = _mock_github_response(releases)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=mock_resp)
    result = list_remote_versions(limit=5)
    assert result == ["1.5.0", "1.4.0"]


def test_list_remote_versions_strips_v_prefix(mocker: MockerFixture):
    releases = [{"tag_name": "v2.0.0"}]
    mock_resp = _mock_github_response(releases)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=mock_resp)
    result = list_remote_versions()
    assert result == ["2.0.0"]


def test_list_remote_versions_skips_non_string_tags(mocker: MockerFixture):
    releases = [
        {"tag_name": "v1.0.0"},
        {"tag_name": None},
        {"other_key": "v2.0.0"},
    ]
    mock_resp = _mock_github_response(releases)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=mock_resp)
    result = list_remote_versions()
    assert result == ["1.0.0"]


def test_list_remote_versions_network_error(mocker: MockerFixture):
    mocker.patch("mvmctl.core.binary_manager.urlopen", side_effect=URLError("timeout"))
    with pytest.raises(BinaryError, match="Failed to fetch releases"):
        list_remote_versions()


def test_list_remote_versions_os_error(mocker: MockerFixture):
    mocker.patch("mvmctl.core.binary_manager.urlopen", side_effect=OSError("connection reset"))
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
    result = fetch_binary("1.0.0", tmp_path)
    assert result.version == "1.0.0"
    assert result.firecracker_path == tmp_path / "firecracker-v1.0.0"
    assert result.jailer_path == tmp_path / "jailer-v1.0.0"


def test_fetch_binary_already_exists_normalizes_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    result = fetch_binary("v1.0.0", tmp_path)
    assert result.version == "1.0.0"


def test_fetch_binary_already_exists_with_active_symlink(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")

    # Without set_as_default, should detect active via symlink
    result = fetch_binary("1.0.0", tmp_path)
    assert result.is_active is True


def test_fetch_binary_already_exists_not_active(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    # No symlink

    result = fetch_binary("1.0.0", tmp_path)
    assert result.is_active is False


def test_fetch_binary_already_exists_with_set_as_default(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()

    result = fetch_binary("1.0.0", tmp_path, set_as_default=True)
    assert result.is_active is True


def test_fetch_binary_downloads_and_extracts(tmp_path: Path, mocker: MockerFixture):
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

    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)
    mocker.patch("mvmctl.core.binary_manager.set_active_version")
    result = fetch_binary("1.5.0", tmp_path)

    assert result.version == "1.5.0"
    assert result.firecracker_path.exists()
    assert result.jailer_path.exists()
    assert os.access(result.firecracker_path, os.X_OK)
    assert os.access(result.jailer_path, os.X_OK)
    # Tarball should be cleaned up
    assert not (tmp_path / "firecracker-v1.5.0-x86_64.tgz").exists()


def test_fetch_binary_download_failure_cleans_up(tmp_path: Path, mocker: MockerFixture):
    # Mock SHA256 sidecar fetch to return a valid checksum
    sha_resp = MagicMock()
    sha_resp.read.return_value = b"abc123...  file.tgz\n"
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)

    # Mock actual download to fail
    mocker.patch("mvmctl.utils.http.urlopen", side_effect=URLError("network error"))
    with pytest.raises(BinaryError, match="Failed to download"):
        fetch_binary("1.5.0", tmp_path)
    # No partial tgz left behind
    assert not (tmp_path / "firecracker-v1.5.0-x86_64.tgz").exists()


def test_fetch_binary_missing_binaries_in_archive(tmp_path: Path, mocker: MockerFixture):
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

    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)
    with pytest.raises(BinaryError, match="missing expected binaries"):
        fetch_binary("1.5.0", tmp_path)


def test_fetch_binary_corrupt_tarball(tmp_path: Path, mocker: MockerFixture):
    sha_resp = MagicMock()
    sha_resp.read.return_value = b"abc123  file.tgz\n"
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)

    mocker.patch(
        "mvmctl.core.binary_manager.download_with_progress",
        side_effect=BinaryError("network error"),
    )
    with pytest.raises(BinaryError, match="Failed to download"):
        fetch_binary("1.5.0", tmp_path)


def test_fetch_binary_sets_executable_permissions(tmp_path: Path, mocker: MockerFixture):
    tarball_data = _make_tarball(tmp_path, "1.5.0")
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

    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)
    mocker.patch("mvmctl.core.binary_manager.set_active_version")

    result = fetch_binary("1.5.0", tmp_path)
    assert os.access(result.firecracker_path, os.X_OK)
    assert os.access(result.jailer_path, os.X_OK)


def test_fetch_binary_extract_failure_cleans_up(tmp_path: Path, mocker: MockerFixture):
    sha_resp = MagicMock()
    sha_resp.read.return_value = b"abc123  file.tgz\n"
    sha_resp.__enter__ = lambda s: s
    sha_resp.__exit__ = MagicMock(return_value=False)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)

    mocker.patch(
        "mvmctl.core.binary_manager.download_with_progress",
        side_effect=BinaryError("network"),
    )
    with pytest.raises(BinaryError):
        fetch_binary("1.5.0", tmp_path)
    assert not (tmp_path / "firecracker-v1.5.0").exists()
    assert not (tmp_path / "jailer-v1.5.0").exists()


def test_fetch_binary_checksum_mismatch_raises(tmp_path: Path, mocker: MockerFixture):
    tarball_data = _make_tarball(tmp_path, "1.5.0")

    def mock_urlopen(req, **kwargs):
        if hasattr(req, "full_url") and req.full_url.endswith(".sha256.txt"):
            sha_resp = MagicMock()
            sha_resp.read.return_value = b"invalidhash123  file.tgz\n"
            sha_resp.__enter__ = lambda s: s
            sha_resp.__exit__ = MagicMock(return_value=False)
            return sha_resp
        else:
            mock_resp = MagicMock()
            mock_resp.read.side_effect = [tarball_data, b""]
            mock_resp.__enter__ = lambda s: s
            mock_resp.__exit__ = MagicMock(return_value=False)
            return mock_resp

    mocker.patch("mvmctl.utils.http.urlopen", side_effect=mock_urlopen)
    mocker.patch("mvmctl.core.binary_manager.urlopen", side_effect=mock_urlopen)
    with pytest.raises(BinaryError, match="Checksum mismatch"):
        fetch_binary("1.5.0", tmp_path)


def test_fetch_binary_sha256_sidecar_unavailable(tmp_path: Path, mocker: MockerFixture):
    tarball_data = _make_tarball(tmp_path, "1.6.0")
    mock_resp = MagicMock()
    mock_resp.read.side_effect = [tarball_data, b""]
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)

    mock_download = mocker.patch(
        "mvmctl.core.binary_manager.download_with_progress", return_value=True
    )
    mocker.patch("mvmctl.core.binary_manager.urlopen", side_effect=URLError("404"))
    with pytest.raises(BinaryError, match="Checksum required"):
        fetch_binary("1.6.0", tmp_path)
    mock_download.assert_not_called()


# ---------------------------------------------------------------------------
# get_binary_path
# ---------------------------------------------------------------------------


def test_get_binary_path_returns_path(tmp_path: Path, mocker: MockerFixture):
    fc_file = tmp_path / "firecracker-v1.15.0"
    fc_file.write_bytes(b"\x7fELF")
    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path)
    result = get_binary_path("firecracker", "1.15.0")
    assert result == str(fc_file)


def test_get_binary_path_normalizes_version(tmp_path: Path, mocker: MockerFixture):
    fc_file = tmp_path / "firecracker-v1.15.0"
    fc_file.write_bytes(b"\x7fELF")
    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path)
    result = get_binary_path("firecracker", "v1.15.0")
    assert result == str(fc_file)


def test_get_binary_path_jailer(tmp_path: Path, mocker: MockerFixture):
    jl_file = tmp_path / "jailer-v1.15.0"
    jl_file.write_bytes(b"\x7fELF")
    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path)
    result = get_binary_path("jailer", "1.15.0")
    assert result == str(jl_file)


def test_get_binary_path_not_found_raises(tmp_path: Path, mocker: MockerFixture):
    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path)
    with pytest.raises(AssetNotFoundError, match="Binary 'firecracker' version '9.9.9' not found"):
        get_binary_path("firecracker", "9.9.9")


def test_get_binary_path_unknown_name_raises(tmp_path: Path, mocker: MockerFixture):
    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path)
    with pytest.raises(AssetNotFoundError, match="Unknown binary name: unknown"):
        get_binary_path("unknown", "1.0.0")


# ---------------------------------------------------------------------------
# set_active_version
# ---------------------------------------------------------------------------


def test_set_active_version_verifies_binaries_exist(tmp_path: Path, mocker: MockerFixture):
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    set_active_version("1.0.0", tmp_path)

    # set_active_version no longer creates symlinks; SQLite is canonical for defaults
    fc_link = tmp_path / "firecracker"
    jl_link = tmp_path / "jailer"
    assert not fc_link.exists()
    assert not jl_link.exists()


def test_set_active_version_does_not_update_symlinks(tmp_path: Path, mocker: MockerFixture):
    fc_v1 = tmp_path / "firecracker-v1.0.0"
    jl_v1 = tmp_path / "jailer-v1.0.0"
    fc_v2 = tmp_path / "firecracker-v2.0.0"
    jl_v2 = tmp_path / "jailer-v2.0.0"
    fc_v1.touch()
    jl_v1.touch()
    fc_v2.touch()
    jl_v2.touch()

    # set_active_version no longer modifies symlinks; SQLite is canonical
    set_active_version("2.0.0", tmp_path)

    # No symlinks should be created
    assert not (tmp_path / "firecracker").exists()
    assert not (tmp_path / "jailer").exists()


def test_set_active_version_missing_binary_raises(tmp_path: Path):
    with pytest.raises(AssetNotFoundError, match="Version 1.0.0 not downloaded"):
        set_active_version("1.0.0", tmp_path)


def test_set_active_version_normalizes_version(tmp_path: Path, mocker: MockerFixture):
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    set_active_version("v1.0.0", tmp_path)

    # set_active_version no longer creates symlinks; SQLite is canonical for defaults
    assert not (tmp_path / "firecracker").exists()


# ---------------------------------------------------------------------------
# remove_version
# ---------------------------------------------------------------------------


def test_remove_version_deletes_files_only(tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    # Create symlinks (these should NOT be removed by remove_version anymore)
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")

    remove_version("1.0.0", tmp_path)

    # Binary files are removed
    assert not fc_file.exists()
    assert not jl_file.exists()
    # Symlinks are NOT removed (remove_version no longer touches symlinks)
    assert (tmp_path / "firecracker").is_symlink()
    assert (tmp_path / "jailer").is_symlink()


def test_remove_version_not_found_raises(tmp_path: Path):
    with pytest.raises(AssetNotFoundError, match="Version 1.0.0 not found locally"):
        remove_version("1.0.0", tmp_path)


def test_remove_version_normalizes_version(tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    remove_version("v1.0.0", tmp_path)

    assert not fc_file.exists()
    assert not jl_file.exists()


def test_remove_version_only_fc_exists(tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.0.0"
    fc_file.touch()

    remove_version("1.0.0", tmp_path)

    assert not fc_file.exists()


def test_remove_version_only_jl_exists(tmp_path: Path):
    jl_file = tmp_path / "jailer-v1.0.0"
    jl_file.touch()

    remove_version("1.0.0", tmp_path)

    assert not jl_file.exists()


def test_remove_version_unrelated_symlinks_preserved(tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    # Create symlinks pointing to different version
    (tmp_path / "firecracker").symlink_to("firecracker-v2.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v2.0.0")

    remove_version("1.0.0", tmp_path)

    # Symlinks should remain since they point to different version
    assert (tmp_path / "firecracker").is_symlink()
    assert (tmp_path / "jailer").is_symlink()


# ---------------------------------------------------------------------------
# _active_target
# ---------------------------------------------------------------------------


def test_active_target_returns_target(tmp_path: Path):
    symlink = tmp_path / "firecracker"
    symlink.symlink_to("firecracker-v1.0.0")
    result = _active_target(symlink)
    assert result == "firecracker-v1.0.0"


def test_active_target_not_symlink_returns_none(tmp_path: Path):
    regular_file = tmp_path / "firecracker"
    regular_file.touch()
    result = _active_target(regular_file)
    assert result is None


def test_active_target_missing_returns_none(tmp_path: Path):
    missing = tmp_path / "firecracker"
    result = _active_target(missing)
    assert result is None
