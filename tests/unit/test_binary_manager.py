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
    _list_local_versions_from_fs,
    _normalize_version,
    ensure_default_binary,
    fetch_binary,
    get_bin_dir,
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
# get_bin_dir
# ---------------------------------------------------------------------------


def test_get_bin_dir_returns_path_under_cache(tmp_path: Path, mocker: MockerFixture):
    result = get_bin_dir()
    assert result == tmp_path / "cache" / "bin"


# ---------------------------------------------------------------------------
# list_local_versions — canonical SQLite-backed path
# (production path: called without bin_dir, reads MVMDatabase)
# ---------------------------------------------------------------------------


def test_list_local_versions_empty_sqlite(mocker: MockerFixture) -> None:
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = []
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    assert list_local_versions() == []


def test_list_local_versions_paired_entries_in_sqlite(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = False

    mock_jl = MagicMock()
    mock_jl.version = "1.0.0"
    mock_jl.path = str(jl_file)
    mock_jl.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    assert len(result) == 1
    assert result[0].version == "1.0.0"
    assert result[0].firecracker_path == fc_file
    assert result[0].jailer_path == jl_file
    assert result[0].is_active is False


def test_list_local_versions_no_jailer_match_skipped(mocker: MockerFixture, tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    fc_file.touch()

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else []
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    assert list_local_versions() == []


def test_list_local_versions_is_active_from_is_default_column(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = True

    mock_jl = MagicMock()
    mock_jl.version = "1.0.0"
    mock_jl.path = str(jl_file)
    mock_jl.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    assert len(result) == 1
    assert result[0].is_active is True


def test_list_local_versions_inactive_when_is_default_false(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = False

    mock_jl = MagicMock()
    mock_jl.version = "1.0.0"
    mock_jl.path = str(jl_file)
    mock_jl.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    assert len(result) == 1
    assert result[0].is_active is False


def test_list_local_versions_multiple_versions_sorted(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    versions = ["1.0.0", "1.1.0", "2.0.0"]
    fc_mocks = []
    jl_mocks = []
    for ver in versions:
        fc_file = tmp_path / f"firecracker-v{ver}"
        jl_file = tmp_path / f"jailer-v{ver}"
        fc_file.touch()
        jl_file.touch()

        mock_fc = MagicMock()
        mock_fc.version = ver
        mock_fc.path = str(fc_file)
        mock_fc.is_default = False
        fc_mocks.append(mock_fc)

        mock_jl = MagicMock()
        mock_jl.version = ver
        mock_jl.path = str(jl_file)
        mock_jl.is_default = False
        jl_mocks.append(mock_jl)

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        fc_mocks if name == "firecracker" else jl_mocks
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    assert [r.version for r in result] == ["2.0.0", "1.1.0", "1.0.0"]


def test_list_local_versions_multiple_versions_active_flag(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    versions_active = [("1.0.0", False), ("2.0.0", True)]
    fc_mocks = []
    jl_mocks = []
    for ver, is_default in versions_active:
        fc_file = tmp_path / f"firecracker-v{ver}"
        jl_file = tmp_path / f"jailer-v{ver}"
        fc_file.touch()
        jl_file.touch()

        mock_fc = MagicMock()
        mock_fc.version = ver
        mock_fc.path = str(fc_file)
        mock_fc.is_default = is_default
        fc_mocks.append(mock_fc)

        mock_jl = MagicMock()
        mock_jl.version = ver
        mock_jl.path = str(jl_file)
        mock_jl.is_default = is_default
        jl_mocks.append(mock_jl)

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        fc_mocks if name == "firecracker" else jl_mocks
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    active = [r for r in result if r.is_active]
    inactive = [r for r in result if not r.is_active]
    assert len(active) == 1
    assert active[0].version == "2.0.0"
    assert len(inactive) == 1
    assert inactive[0].version == "1.0.0"


def test_list_local_versions_skips_missing_files(mocker: MockerFixture, tmp_path: Path) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = False

    mock_jl = MagicMock()
    mock_jl.version = "1.0.0"
    mock_jl.path = str(jl_file)
    mock_jl.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    assert list_local_versions() == []


def test_list_local_versions_does_not_read_symlink_for_active(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    fc_file = tmp_path / "firecracker-v1.0.0"
    jl_file = tmp_path / "jailer-v1.0.0"
    fc_file.touch()
    jl_file.touch()
    symlink = tmp_path / "firecracker"
    symlink.symlink_to("firecracker-v1.0.0")

    mock_fc = MagicMock()
    mock_fc.version = "1.0.0"
    mock_fc.path = str(fc_file)
    mock_fc.is_default = False

    mock_jl = MagicMock()
    mock_jl.version = "1.0.0"
    mock_jl.path = str(jl_file)
    mock_jl.is_default = False

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)

    result = list_local_versions()
    assert len(result) == 1
    assert result[0].is_active is False


# ---------------------------------------------------------------------------
# _list_local_versions_from_fs — filesystem-discovery helper tests
# NOT the canonical production path; called only with an explicit bin_dir.
# ---------------------------------------------------------------------------


def test_list_local_versions_from_fs_ignores_directories(tmp_path: Path) -> None:
    (tmp_path / "firecracker-v1.0.0").mkdir()
    (tmp_path / "jailer-v1.0.0").touch()
    assert _list_local_versions_from_fs(tmp_path) == []


def test_list_local_versions_from_fs_ignores_symlinks(tmp_path: Path) -> None:
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    result = _list_local_versions_from_fs(tmp_path)
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
    result = fetch_binary("1.0.0", bin_dir=tmp_path)
    assert result.version == "1.0.0"
    assert result.firecracker_path == tmp_path / "firecracker-v1.0.0"
    assert result.jailer_path == tmp_path / "jailer-v1.0.0"


def test_fetch_binary_already_exists_normalizes_version(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    result = fetch_binary("v1.0.0", bin_dir=tmp_path)
    assert result.version == "1.0.0"


def test_fetch_binary_already_exists_with_active_symlink(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    mock_binary = MagicMock()
    mock_binary.version = "1.0.0"
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = mock_binary
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = fetch_binary("1.0.0", bin_dir=tmp_path)
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
    mocker.patch("mvmctl.core.binary_manager.update_binary_entry")
    mocker.patch("mvmctl.core.binary_manager.set_active_version")
    result = fetch_binary("1.5.0", bin_dir=tmp_path)

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
        fetch_binary("1.5.0", bin_dir=tmp_path)
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
        fetch_binary("1.5.0", bin_dir=tmp_path)


def test_fetch_binary_corrupt_archive(tmp_path: Path, mocker: MockerFixture):
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

    mocker.patch("mvmctl.utils.http.urlopen", return_value=mock_resp)
    mocker.patch("mvmctl.core.binary_manager.urlopen", return_value=sha_resp)
    with pytest.raises(BinaryError):
        fetch_binary("1.5.0", bin_dir=tmp_path)
    # Partial files cleaned up
    assert not (tmp_path / "firecracker-v1.5.0").exists()
    assert not (tmp_path / "jailer-v1.5.0").exists()


# ---------------------------------------------------------------------------
# set_active_version
# ---------------------------------------------------------------------------


def test_set_active_version_updates_database(tmp_path: Path, mocker: MockerFixture):
    """Verify set_active_version updates the database entries."""
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    mock_update = mocker.patch("mvmctl.core.binary_manager.update_binary_entry")
    mock_set_default = mocker.patch("mvmctl.core.binary_manager.set_default_binary_entry")
    set_active_version("1.0.0", bin_dir=tmp_path)
    mock_update.assert_called_once()
    mock_set_default.assert_called_once()


def test_set_active_version_normalizes_version(tmp_path: Path, mocker: MockerFixture):
    """Verify version normalization works correctly."""
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    mock_update = mocker.patch("mvmctl.core.binary_manager.update_binary_entry")
    mock_set_default = mocker.patch("mvmctl.core.binary_manager.set_default_binary_entry")
    set_active_version("v1.0.0", bin_dir=tmp_path)
    mock_update.assert_called_once()
    mock_set_default.assert_called_once()


def test_set_active_version_updates_default_when_changed(tmp_path: Path, mocker: MockerFixture):
    """Verify database is updated when active version changes."""
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker-v2.0.0").touch()
    (tmp_path / "jailer-v2.0.0").touch()
    mock_update = mocker.patch("mvmctl.core.binary_manager.update_binary_entry")
    mock_set_default = mocker.patch("mvmctl.core.binary_manager.set_default_binary_entry")
    set_active_version("1.0.0", bin_dir=tmp_path)
    set_active_version("2.0.0", bin_dir=tmp_path)
    assert mock_update.call_count == 2
    assert mock_set_default.call_count == 2


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


def test_remove_version_deletes_files(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()
    assert not (tmp_path / "jailer-v1.0.0").exists()


def test_remove_version_normalizes_version(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("v1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()


def test_remove_version_not_found(tmp_path: Path):
    with pytest.raises(AssetNotFoundError, match="not found locally"):
        remove_version("9.9.9", bin_dir=tmp_path)


def test_remove_active_version_removes_symlinks(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker").exists()
    assert not (tmp_path / "jailer").exists()
    assert not (tmp_path / "firecracker-v1.0.0").exists()
    assert not (tmp_path / "jailer-v1.0.0").exists()


def test_remove_version_leaves_other_symlinks(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker-v2.0.0").touch()
    (tmp_path / "jailer-v2.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v2.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v2.0.0")
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    assert (tmp_path / "firecracker").is_symlink()
    assert (tmp_path / "jailer").is_symlink()
    assert os.readlink(tmp_path / "firecracker") == "firecracker-v2.0.0"


def test_remove_version_partial_files(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    assert not (tmp_path / "firecracker-v1.0.0").exists()


def test_remove_version_purges_sqlite_rows(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    mock_db.delete_binary_by_name_and_version.assert_any_call("firecracker", "1.0.0")
    mock_db.delete_binary_by_name_and_version.assert_any_call("jailer", "1.0.0")
    assert mock_db.delete_binary_by_name_and_version.call_count == 2


def test_remove_default_version_clears_sqlite_default(tmp_path: Path, mocker: MockerFixture):
    (tmp_path / "firecracker-v1.0.0").touch()
    (tmp_path / "jailer-v1.0.0").touch()
    (tmp_path / "firecracker").symlink_to("firecracker-v1.0.0")
    (tmp_path / "jailer").symlink_to("jailer-v1.0.0")
    mock_db = MagicMock()
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    remove_version("1.0.0", bin_dir=tmp_path)
    mock_db.delete_binary_by_name_and_version.assert_any_call("firecracker", "1.0.0")
    mock_db.delete_binary_by_name_and_version.assert_any_call("jailer", "1.0.0")
    assert not (tmp_path / "firecracker").exists()
    assert not (tmp_path / "jailer").exists()


# ---------------------------------------------------------------------------
# _extract_member --- None reader
# ---------------------------------------------------------------------------


def test_extract_member_none_reader():
    from mvmctl.core.binary_manager import _extract_member

    mock_tar = MagicMock(spec=tarfile.TarFile)
    mock_member = MagicMock(spec=tarfile.TarInfo)
    mock_member.name = "some-dir/"
    mock_tar.extractfile.return_value = None

    with pytest.raises(BinaryError, match="Cannot read"):
        _extract_member(mock_tar, mock_member, Path("/tmp/dest"))


# ---------------------------------------------------------------------------
# _resolve_bin_dir
# ---------------------------------------------------------------------------


def test_resolve_bin_dir_with_none_uses_default(tmp_path: Path, mocker: MockerFixture):
    from mvmctl.core.binary_manager import _resolve_bin_dir

    mocker.patch("mvmctl.core.binary_manager.get_bin_dir", return_value=tmp_path / "bins")
    result = _resolve_bin_dir(None)
    assert result == tmp_path / "bins"
    assert result.exists()


def test_resolve_bin_dir_with_explicit_path(tmp_path: Path):
    from mvmctl.core.binary_manager import _resolve_bin_dir

    custom = tmp_path / "custom" / "bin"
    result = _resolve_bin_dir(custom)
    assert result == custom
    assert result.exists()


# ---------------------------------------------------------------------------
# _active_target — private helper for filesystem-discovery path only
# NOT called in the canonical SQLite path
# ---------------------------------------------------------------------------


def test_active_target_symlink_exists(tmp_path: Path):
    (tmp_path / "firecracker-v1.0.0").touch()
    link = tmp_path / "firecracker"
    link.symlink_to("firecracker-v1.0.0")
    assert _active_target(link) == "firecracker-v1.0.0"


def test_active_target_not_a_symlink(tmp_path: Path):
    regular_file = tmp_path / "firecracker"
    regular_file.touch()
    assert _active_target(regular_file) is None


def test_active_target_path_does_not_exist(tmp_path: Path):
    assert _active_target(tmp_path / "nonexistent") is None


# ---------------------------------------------------------------------------
# S-H3: SHA-256 verification tests
# ---------------------------------------------------------------------------


def test_fetch_binary_sha256_mismatch(tmp_path: Path, mocker: MockerFixture):
    """SHA-256 mismatch with sidecar should raise BinaryError."""
    tarball_data = _make_tarball(tmp_path, "1.5.0")

    def mock_urlopen(req, **kwargs):
        url = str(req.full_url if hasattr(req, "full_url") else req)
        mock_resp = MagicMock()
        if ".sha256.txt" in url:
            mock_resp.read.return_value = (
                b"0000000000000000000000000000000000000000000000000000000000000000  file.tgz\n"
            )
        else:
            mock_resp.read.side_effect = [tarball_data, b""]
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        return mock_resp

    mocker.patch("mvmctl.utils.http.urlopen", side_effect=mock_urlopen)
    mocker.patch("mvmctl.core.binary_manager.urlopen", side_effect=mock_urlopen)
    with pytest.raises(BinaryError, match="Checksum mismatch"):
        fetch_binary("1.5.0", bin_dir=tmp_path)


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
        fetch_binary("1.6.0", bin_dir=tmp_path)
    mock_download.assert_not_called()


# ---------------------------------------------------------------------------
# get_binary_path
# ---------------------------------------------------------------------------


def test_get_binary_path_returns_default_path(mocker: MockerFixture, tmp_path: Path):
    fc_file = tmp_path / "firecracker"
    fc_file.write_bytes(b"\x7fELF")
    mock_binary = MagicMock()
    mock_binary.path = str(fc_file)
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = mock_binary
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = get_binary_path("firecracker")
    assert result == str(fc_file)
    mock_db.get_default_binary.assert_called_once_with("firecracker")


def test_get_binary_path_no_default_raises(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = None
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="No active binary for 'firecracker'"):
        get_binary_path("firecracker")


def test_get_binary_path_no_default_jailer_raises(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = None
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="No active binary for 'jailer'"):
        get_binary_path("jailer")


def test_get_binary_path_default_empty_path_raises(mocker: MockerFixture):
    mock_binary = MagicMock()
    mock_binary.path = ""
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = mock_binary
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="No active binary for 'firecracker'"):
        get_binary_path("firecracker")


def test_get_binary_path_specific_version_found(mocker: MockerFixture, tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.15.0"
    fc_file.write_bytes(b"\x7fELF")
    mock_binary = MagicMock()
    mock_binary.version = "1.15.0"
    mock_binary.full_version = "v1.15.0"
    mock_binary.path = str(fc_file)
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = [mock_binary]
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = get_binary_path("firecracker", version="1.15.0")
    assert result == str(fc_file)


def test_get_binary_path_specific_version_with_v_prefix(mocker: MockerFixture, tmp_path: Path):
    fc_file = tmp_path / "firecracker-v1.15.0"
    fc_file.write_bytes(b"\x7fELF")
    mock_binary = MagicMock()
    mock_binary.version = "1.15.0"
    mock_binary.full_version = "v1.15.0"
    mock_binary.path = str(fc_file)
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = [mock_binary]
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = get_binary_path("firecracker", version="v1.15.0")
    assert result == str(fc_file)


def test_get_binary_path_specific_version_not_found_raises(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = []
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="Binary 'firecracker' version '9.9.9' not found"):
        get_binary_path("firecracker", version="9.9.9")


def test_get_binary_path_error_message_includes_fetch_hint(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = None
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="mvm bin fetch"):
        get_binary_path("firecracker")


def test_get_binary_path_version_not_found_error_includes_version(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = []
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="2.0.0"):
        get_binary_path("firecracker", version="2.0.0")


def test_get_binary_path_stale_default_path_raises(mocker: MockerFixture, tmp_path: Path):
    mock_binary = MagicMock()
    mock_binary.path = str(tmp_path / "firecracker-v1.15.0-DELETED")
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = mock_binary
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="missing"):
        get_binary_path("firecracker")


def test_get_binary_path_stale_version_path_raises(mocker: MockerFixture, tmp_path: Path):
    mock_binary = MagicMock()
    mock_binary.version = "1.15.0"
    mock_binary.full_version = "v1.15.0"
    mock_binary.path = str(tmp_path / "firecracker-v1.15.0-DELETED")
    mock_db = MagicMock()
    mock_db.list_binaries_by_name.return_value = [mock_binary]
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="missing"):
        get_binary_path("firecracker", version="1.15.0")


def test_get_binary_path_no_default_error_mentions_set_default(mocker: MockerFixture):
    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = None
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    with pytest.raises(AssetNotFoundError, match="mvm bin set-default"):
        get_binary_path("firecracker")


# ---------------------------------------------------------------------------
# ensure_default_binary
# ---------------------------------------------------------------------------


def test_ensure_default_binary_no_locals_returns_none(mocker: MockerFixture):
    mocker.patch("mvmctl.core.binary_manager.list_local_versions", return_value=[])
    result = ensure_default_binary()
    assert result is None


def test_ensure_default_binary_default_already_set(mocker: MockerFixture):
    from mvmctl.core.binary_manager import BinaryVersion

    bv = BinaryVersion(
        version="1.15.0",
        firecracker_path=Path("/cache/bin/firecracker-v1.15.0"),
        jailer_path=Path("/cache/bin/jailer-v1.15.0"),
        is_active=True,
    )
    mocker.patch("mvmctl.core.binary_manager.list_local_versions", return_value=[bv])
    mock_db_instance = MagicMock()
    mock_existing = MagicMock()
    mock_existing.path = "/cache/bin/firecracker"
    mock_existing.version = "1.15.0"
    mock_db_instance.get_default_binary.return_value = mock_existing
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db_instance)
    result = ensure_default_binary()
    assert result == "1.15.0"


def test_ensure_default_binary_repairs_missing_default(mocker: MockerFixture, tmp_path: Path):
    from mvmctl.core.binary_manager import BinaryVersion

    fc_file = tmp_path / "firecracker-v1.15.0"
    jl_file = tmp_path / "jailer-v1.15.0"
    fc_file.write_bytes(b"\x7fELF")
    jl_file.write_bytes(b"\x7fELF")

    bv = BinaryVersion(
        version="1.15.0",
        firecracker_path=fc_file,
        jailer_path=jl_file,
        is_active=False,
    )
    mocker.patch("mvmctl.core.binary_manager.list_local_versions", return_value=[bv])
    mock_db_instance = MagicMock()
    mock_db_instance.get_default_binary.return_value = None
    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db_instance)
    mock_set_active = mocker.patch("mvmctl.core.binary_manager.set_active_version")

    result = ensure_default_binary(bin_dir=tmp_path)

    assert result == "1.15.0"
    mock_set_active.assert_called_once_with("1.15.0", tmp_path)


def test_list_local_versions_no_bin_dir_uses_sqlite(mocker: MockerFixture, tmp_path: Path) -> None:
    fc_path = tmp_path / "firecracker-v1.15.0"
    jl_path = tmp_path / "jailer-v1.15.0"
    fc_path.touch()
    jl_path.touch()

    mock_fc = MagicMock()
    mock_fc.version = "1.15.0"
    mock_fc.path = str(fc_path)
    mock_fc.is_default = True

    mock_jl = MagicMock()
    mock_jl.version = "1.15.0"
    mock_jl.path = str(jl_path)
    mock_jl.is_default = True

    mock_db = MagicMock()
    mock_db.list_binaries_by_name.side_effect = lambda name: (
        [mock_fc] if name == "firecracker" else [mock_jl]
    )

    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = list_local_versions()

    assert len(result) == 1
    assert result[0].is_active is True
    assert result[0].version == "1.15.0"


def test_ensure_default_binary_skips_scan_when_sqlite_default_exists(
    mocker: MockerFixture, tmp_path: Path
) -> None:
    fc_path = tmp_path / "firecracker-v1.15.0"
    fc_path.touch()

    mock_default = MagicMock()
    mock_default.version = "1.15.0"
    mock_default.path = str(fc_path)

    mock_db = MagicMock()
    mock_db.get_default_binary.return_value = mock_default

    mock_list = mocker.patch("mvmctl.core.binary_manager.list_local_versions")

    mocker.patch("mvmctl.core.binary_manager.MVMDatabase", return_value=mock_db)
    result = ensure_default_binary()

    assert result == "1.15.0"
    mock_list.assert_not_called()
