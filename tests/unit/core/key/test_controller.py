"""Tests for KeyController — SSH key lifecycle management."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.core.key._controller import KeyController
from mvmctl.exceptions import KeyExportError
from mvmctl.models import SSHKeyItem


def _make_key(
    key_id: str = "sha256-abc123",
    name: str = "test-key",
) -> SSHKeyItem:
    """Build a minimal SSHKeyItem with all required fields."""
    return SSHKeyItem(
        id=key_id,
        name=name,
        fingerprint=key_id,
        algorithm="ssh-ed25519",
        comment="test@host",
        public_key_path=f"/tmp/keys/{name}.pub",
        private_key_path=None,
        is_default=False,
        is_present=True,
        created_at="2025-01-01T00:00:00",
        updated_at="2025-01-01T00:00:00",
    )


SAMPLE_KEY = _make_key()


class TestKeyControllerInit:
    def test_with_sshkeyitem(self):
        """__init__ accepts an SSHKeyItem directly."""
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)
        assert controller._key == SAMPLE_KEY
        assert controller._repo is repo

    def test_with_string_entity(self):
        """__init__ resolves a string via KeyResolver when no SSHKeyItem is given."""
        repo = MagicMock()
        repo.get_by_name = MagicMock(return_value=None)  # Fall through to by_id
        resolved = _make_key(key_id="sha256-resolved", name="resolved-key")
        repo.find_by_prefix = MagicMock(return_value=[resolved])

        controller = KeyController("resolved-key", repo)

        assert controller._key == resolved
        repo.find_by_prefix.assert_called_once()

    def test_key_id_property(self):
        """key_id returns the model's id."""
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)
        assert controller.key_id == "sha256-abc123"

    def test_key_name_property(self):
        """key_name returns the model's name."""
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)
        assert controller.key_name == "test-key"

    def test_inspect(self):
        """inspect returns the underlying SSHKeyItem."""
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)
        assert controller.inspect() == SAMPLE_KEY


class TestKeyControllerExport:
    def test_export_success(self, tmp_path: Path) -> None:
        """Export copies both key files to destination and sets 0600 perms."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        (keys_dir / "test-key").write_text("private key content")
        (keys_dir / "test-key.pub").write_text("public key content")

        dest = tmp_path / "export"
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)

        result = controller.export(dest, keys_dir=keys_dir)

        assert len(result) == 2
        assert (dest / "test-key").exists()
        assert (dest / "test-key.pub").exists()
        assert (dest / "test-key").read_text() == "private key content"
        # Verify private key permissions
        assert (dest / "test-key").stat().st_mode & 0o777 == 0o600

    def test_export_missing_private_key(self, tmp_path: Path) -> None:
        """Raises KeyExportError when private key is missing."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        (keys_dir / "test-key.pub").write_text("public key content")

        dest = tmp_path / "export"
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)

        with pytest.raises(KeyExportError, match="Private key not found"):
            controller.export(dest, keys_dir=keys_dir)

    def test_export_missing_public_key(self, tmp_path: Path) -> None:
        """Raises KeyExportError when public key is missing."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        (keys_dir / "test-key").write_text("private key content")

        dest = tmp_path / "export"
        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)

        with pytest.raises(KeyExportError, match="Public key not found"):
            controller.export(dest, keys_dir=keys_dir)

    def test_export_without_overwrite_when_dest_exists(
        self, tmp_path: Path
    ) -> None:
        """Raises KeyExportError when destination files exist and overwrite=False."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        (keys_dir / "test-key").write_text("private")
        (keys_dir / "test-key.pub").write_text("public")

        dest = tmp_path / "export"
        dest.mkdir()
        (dest / "test-key").write_text("existing")
        (dest / "test-key.pub").write_text("existing")

        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)

        with pytest.raises(KeyExportError, match="already exist"):
            controller.export(dest, keys_dir=keys_dir, overwrite=False)

    def test_export_with_overwrite(self, tmp_path: Path) -> None:
        """Export overwrites existing files when overwrite=True."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()
        (keys_dir / "test-key").write_text("new private")
        (keys_dir / "test-key.pub").write_text("new public")

        dest = tmp_path / "export"
        dest.mkdir()
        (dest / "test-key").write_text("old private")
        (dest / "test-key.pub").write_text("old public")

        repo = MagicMock()
        controller = KeyController(SAMPLE_KEY, repo)

        controller.export(dest, keys_dir=keys_dir, overwrite=True)

        assert (dest / "test-key").read_text() == "new private"
