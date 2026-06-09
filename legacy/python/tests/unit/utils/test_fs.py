"""Tests for utils/fs.py — FsUtils filesystem helpers."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.exceptions import MVMError
from mvmctl.utils.fs import FsUtils

# ---------------------------------------------------------------------------
# read_json / read_yaml / read_raw
# ---------------------------------------------------------------------------


class TestReadJson:
    """Tests for FsUtils.read_json()."""

    def test_reads_valid_json(self, tmp_path: Path):
        f = tmp_path / "data.json"
        f.write_text('{"key": "value"}')
        result = FsUtils.read_json(f)
        assert result == {"key": "value"}

    def test_reads_list_json(self, tmp_path: Path):
        f = tmp_path / "list.json"
        f.write_text("[1, 2, 3]")
        result = FsUtils.read_json(f)
        assert result == [1, 2, 3]

    def test_raises_on_invalid_json(self, tmp_path: Path):
        f = tmp_path / "bad.json"
        f.write_text("{invalid")
        with pytest.raises(MVMError, match="Failed to read JSON"):
            FsUtils.read_json(f)

    def test_raises_on_missing_file(self, tmp_path: Path):
        with pytest.raises(FileNotFoundError):
            FsUtils.read_json(tmp_path / "nonexistent.json")


class TestReadYaml:
    """Tests for FsUtils.read_yaml()."""

    def test_reads_valid_yaml(self, tmp_path: Path):
        f = tmp_path / "data.yaml"
        f.write_text("key: value\nfoo: bar")
        result = FsUtils.read_yaml(f)
        assert result == {"key": "value", "foo": "bar"}

    def test_reads_list_yaml(self, tmp_path: Path):
        f = tmp_path / "list.yaml"
        f.write_text("- one\n- two")
        result = FsUtils.read_yaml(f)
        assert result == ["one", "two"]

    def test_returns_empty_dict_for_empty_yaml(self, tmp_path: Path):
        f = tmp_path / "empty.yaml"
        f.write_text("")
        result = FsUtils.read_yaml(f)
        assert result == {}

    def test_raises_on_invalid_yaml(self, tmp_path: Path):
        f = tmp_path / "bad.yaml"
        f.write_text(": invalid yaml :")
        with pytest.raises(MVMError, match="Failed to read YAML"):
            FsUtils.read_yaml(f)

    def test_raises_on_missing_file(self, tmp_path: Path):
        with pytest.raises(FileNotFoundError):
            FsUtils.read_yaml(tmp_path / "nonexistent.yaml")


class TestReadRaw:
    """Tests for FsUtils.read_raw()."""

    def test_reads_text_content(self, tmp_path: Path):
        f = tmp_path / "data.txt"
        f.write_text("hello world")
        result = FsUtils.read_raw(f)
        assert result == "hello world"

    def test_reads_multiline(self, tmp_path: Path):
        f = tmp_path / "data.txt"
        f.write_text("line1\nline2\nline3")
        result = FsUtils.read_raw(f)
        assert result == "line1\nline2\nline3"

    def test_raises_on_missing_file(self, tmp_path: Path):
        with pytest.raises(FileNotFoundError):
            FsUtils.read_raw(tmp_path / "nonexistent.txt")


# ---------------------------------------------------------------------------
# secure_mkdir
# ---------------------------------------------------------------------------


class TestSecureMkdir:
    """Tests for FsUtils.secure_mkdir()."""

    def test_creates_directory(self, tmp_path: Path):
        new_dir = tmp_path / "new_dir"
        FsUtils.secure_mkdir(new_dir, "new_dir")
        assert new_dir.exists()
        assert new_dir.is_dir()

    def test_raises_when_dir_exists(self, tmp_path: Path):
        existing = tmp_path / "existing"
        existing.mkdir()
        with pytest.raises(MVMError, match="already exists"):
            FsUtils.secure_mkdir(existing, "existing")

    def test_raises_on_symlink(self, tmp_path: Path):
        target = tmp_path / "target"
        target.mkdir()
        link = tmp_path / "link"
        link.symlink_to(target)
        with pytest.raises(MVMError, match="symlink"):
            FsUtils.secure_mkdir(link, "link")

    def test_creates_parents(self, tmp_path: Path):
        new_dir = tmp_path / "parent" / "child" / "nested"
        FsUtils.secure_mkdir(new_dir, "nested")
        assert new_dir.exists()


# ---------------------------------------------------------------------------
# get_real_user_ids
# ---------------------------------------------------------------------------


class TestGetRealUserIds:
    """Tests for FsUtils.get_real_user_ids()."""

    @patch("mvmctl.utils.fs.os.getuid", return_value=1000)
    def test_returns_none_when_not_root(self, mock_getuid):
        assert FsUtils.get_real_user_ids() is None

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    @patch.dict("os.environ", {}, clear=True)
    def test_returns_none_when_no_sudo_user(self, mock_getuid):
        assert FsUtils.get_real_user_ids() is None

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    @patch("pwd.getpwnam")
    def test_returns_ids_when_sudo(
        self, mock_getpwnam, mock_getuid, monkeypatch
    ):
        monkeypatch.setenv("SUDO_USER", "testuser")
        mock_getpwnam.return_value = MagicMock(pw_uid=1000, pw_gid=1000)

        result = FsUtils.get_real_user_ids()
        assert result == (1000, 1000)

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    @patch("pwd.getpwnam")
    def test_returns_none_for_invalid_sudo_user(
        self, mock_getpwnam, mock_getuid, monkeypatch
    ):
        monkeypatch.setenv("SUDO_USER", "nonexistent_user_xyz")
        mock_getpwnam.side_effect = KeyError("not found")

        result = FsUtils.get_real_user_ids()
        assert result is None


# ---------------------------------------------------------------------------
# chown_to_real_user
# ---------------------------------------------------------------------------


class TestChownToRealUser:
    """Tests for FsUtils.chown_to_real_user()."""

    @patch("mvmctl.utils.fs.os.getuid", return_value=1000)
    def test_noop_when_not_root(self, mock_getuid, tmp_path: Path):
        target = tmp_path / "test"
        target.write_text("test")
        FsUtils.chown_to_real_user(target)  # Should not raise

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    def test_noop_when_path_not_exists(self, mock_getuid, tmp_path: Path):
        FsUtils.chown_to_real_user(tmp_path / "nonexistent")  # Should not raise

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    def test_chowns_file(self, mock_getuid, monkeypatch, tmp_path: Path):
        monkeypatch.setenv("SUDO_USER", "root")
        chowned = []

        def _fake_chown(path, uid, gid):
            chowned.append(path)

        monkeypatch.setattr("mvmctl.utils.fs.os.chown", _fake_chown)
        f = tmp_path / "test.txt"
        f.write_text("x")
        FsUtils.chown_to_real_user(f)
        assert len(chowned) >= 1

    @patch("mvmctl.utils.fs.os.getuid", return_value=0)
    def test_oserror_is_swallowed(
        self, mock_getuid, monkeypatch, tmp_path: Path
    ):
        monkeypatch.setenv("SUDO_USER", "root")

        def _raise(*args):
            raise OSError("permission denied")

        monkeypatch.setattr("mvmctl.utils.fs.os.chown", _raise)
        f = tmp_path / "test.txt"
        f.write_text("x")
        FsUtils.chown_to_real_user(f)  # Should not raise


# ---------------------------------------------------------------------------
# write_pid_file
# ---------------------------------------------------------------------------


class TestWritePidFile:
    """Tests for FsUtils.write_pid_file()."""

    def test_writes_pid(self, tmp_path: Path):
        pid_file = tmp_path / "test.pid"
        FsUtils.write_pid_file(pid_file, 1234)
        assert pid_file.exists()
        content = pid_file.read_text().strip()
        assert content == "1234"
