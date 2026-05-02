"""Tests for KeyOperation class — SSH key management orchestration."""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.api.inputs._key_create_input import KeyCreateInput
from mvmctl.api.inputs._key_input import KeyInput
from mvmctl.api.key_operations import KeyOperation
from mvmctl.models.result import OperationResult
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import SSHKeyItem


def _make_key(
    name: str = "test-key",
    algorithm: str = "ed25519",
    is_default: bool = False,
    fingerprint: str = "SHA256:abc123",
) -> SSHKeyItem:
    return SSHKeyItem(
        id=f"{name}-id-" + "x" * 55,
        name=name,
        fingerprint=fingerprint,
        algorithm=algorithm,
        comment=f"{name}@host",
        public_key_path=f"/keys/{name}.pub",
        private_key_path=f"/keys/{name}",
        is_default=is_default,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )


class TestKeyOperationListAll:
    """Tests for KeyOperation.list_all()."""

    def test_returns_key_list(self, mocker):
        """list_all() returns SSHKeyItem list from key service."""
        mock_keys = [_make_key("key1"), _make_key("key2")]
        mock_service = mocker.MagicMock()
        mock_service.list_keys.return_value = mock_keys
        # Patch at the point of use
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.CacheUtils.get_keys_dir",
            return_value=Path("/keys"),
        )

        result = KeyOperation.list_all()
        assert len(result) == 2
        mock_service.list_keys.assert_called_once_with(Path("/keys"))

    def test_empty_list(self, mocker):
        """list_all() returns empty list when no keys exist."""
        mock_service = mocker.MagicMock()
        mock_service.list_keys.return_value = []
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.CacheUtils.get_keys_dir",
            return_value=Path("/keys"),
        )

        result = KeyOperation.list_all()
        assert result == []


class TestKeyOperationGet:
    """Tests for KeyOperation.get()."""

    def test_get_by_name(self, mocker):
        """get() returns a single key by name."""
        mock_key = _make_key("my-key")
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(keys=[mock_key])
        # Patch at the point of use
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        result = KeyOperation.get(KeyInput(name=["my-key"]))
        assert result.name == "my-key"
        assert result.algorithm == "ed25519"

    def test_get_raises_when_multiple(self, mocker):
        """get() raises MVMKeyError when multiple keys match."""
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(
            keys=[_make_key("k1"), _make_key("k2")]
        )
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        with pytest.raises(MVMKeyError, match="Expected exactly one key"):
            KeyOperation.get(KeyInput(name=["amb"]))


class TestKeyOperationCreate:
    """Tests for KeyOperation.create()."""

    def test_creates_keypair(self, mocker):
        """create() creates a keypair through core services."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.name = "new-key"
        mock_resolved.output_dir = Path("/keys")
        mock_resolved.algorithm = "ed25519"
        mock_resolved.bits = None
        mock_resolved.comment = "new-key@host"
        mock_resolved.set_default = True
        mock_resolved.overwrite = False

        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved

        mock_key_item = _make_key("new-key", is_default=True)
        mock_service = mocker.MagicMock()
        mock_service.check_dependencies.return_value = None
        mock_service.create_keypair.return_value = [mock_key_item]
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.KeyCreateRequest",
            return_value=mock_request,
        )
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        result = KeyOperation.create(
            KeyCreateInput(name="new-key", set_default=True)
        )

        assert result.item.name == "new-key"
        assert result.item.is_default is True
        mock_service.check_dependencies.assert_called_once()
        mock_service.create_keypair.assert_called_once_with(
            name="new-key",
            output_dir=Path("/keys"),
            algorithm="ed25519",
            bits=None,
            comment="new-key@host",
            is_default=True,
            overwrite=False,
        )
        mock_audit.assert_called_once()


class TestKeyOperationAdd:
    """Tests for KeyOperation.add()."""

    def test_adds_existing_key(self, mocker):
        """add() adds an existing public key to cache."""
        mock_key_item = _make_key("imported-key")
        mock_service = mocker.MagicMock()
        mock_service.add_key.return_value = mock_key_item
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.CacheUtils.get_keys_dir",
            return_value=Path("/keys"),
        )
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        result = KeyOperation.add("imported-key", Path("/tmp/key.pub"))

        assert result.item.name == "imported-key"
        mock_service.add_key.assert_called_once_with(
            "imported-key", Path("/tmp/key.pub"), Path("/keys"), overwrite=False
        )
        mock_audit.assert_called_once()


class TestKeyOperationRemove:
    """Tests for KeyOperation.remove()."""

    def test_removes_key_files_and_db_entry(self, mocker):
        """remove() deletes key files and removes from DB."""
        mock_key = _make_key("remove-me")
        mock_resolved = mocker.MagicMock()
        mock_resolved.keys = [mock_key]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        mock_controller = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.key_operations.KeyController",
            return_value=mock_controller,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.CacheUtils.get_keys_dir",
            return_value=Path("/keys"),
        )
        mock_unlink = mocker.patch.object(Path, "unlink")
        mocker.patch.object(Path, "exists", return_value=True)

        KeyOperation.remove(KeyInput(name=["remove-me"]))

        assert mock_unlink.call_count == 2  # pub + priv files
        mock_controller.remove.assert_called_once()


class TestKeyOperationSetDefault:
    """Tests for KeyOperation.set_default() and related methods."""

    def test_set_default(self, mocker):
        """set_default() marks keys as default via service."""
        mock_key = _make_key("default-key")
        mock_resolved = mocker.MagicMock()
        mock_resolved.keys = [mock_key]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        mock_service = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        KeyOperation.set_default(KeyInput(name=["default-key"]))

        mock_service.set_default_keys.assert_called_once_with(["default-key"])
        mock_audit.assert_called_once()

    def test_get_defaults(self, mocker):
        """get_defaults() returns default keys from repository."""
        mock_keys = [_make_key("default-key", is_default=True)]
        mock_repo = mocker.MagicMock()
        mock_repo.get_defaults.return_value = mock_keys
        mocker.patch(
            "mvmctl.api.key_operations.KeyRepository",
            return_value=mock_repo,
        )

        result = KeyOperation.get_defaults()
        assert result == mock_keys
        mock_repo.get_defaults.assert_called_once()

    def test_clear_defaults(self, mocker):
        """clear_defaults() clears default keys via service."""
        mock_service = mocker.MagicMock()
        mocker.patch(
            "mvmctl.api.key_operations.KeyService",
            return_value=mock_service,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mock_audit = mocker.patch("mvmctl.utils.auditlog.AuditLog.log")

        KeyOperation.clear_defaults()

        mock_service.clear_default_keys.assert_called_once()
        mock_audit.assert_called_once()


class TestKeyOperationInspect:
    """Tests for KeyOperation.inspect()."""

    def test_inspect_returns_key_item(self, mocker):
        """inspect() returns SSHKeyItem when is_json=False."""
        mock_key = _make_key("inspect-me")
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(keys=[mock_key])
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        result = KeyOperation.inspect(
            KeyInput(name=["inspect-me"]), is_json=False
        )

        assert result is mock_key

    def test_inspect_json(self, mocker):
        """inspect() returns dict when is_json=True."""
        mock_key = _make_key("inspect-me")
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(keys=[mock_key])
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        result = KeyOperation.inspect(
            KeyInput(name=["inspect-me"]), is_json=True
        )

        assert isinstance(result, dict)
        assert result["name"] == "inspect-me"
        assert result["fingerprint"] == "SHA256:abc123"
        assert result["is_default"] is False

    def test_inspect_json_with_default(self, mocker):
        """inspect() json output includes is_default."""
        mock_key = _make_key("default-key", is_default=True)
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mocker.MagicMock(keys=[mock_key])
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        result = KeyOperation.inspect(
            KeyInput(name=["default-key"]), is_json=True
        )

        assert result["is_default"] is True


class TestKeyOperationExport:
    """Tests for KeyOperation.export()."""

    def test_export_keypair(self, mocker):
        """export() copies keypair to destination."""
        mock_key = _make_key("export-me")
        mock_resolved = mocker.MagicMock()
        mock_resolved.keys = [mock_key]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )

        mock_controller = mocker.MagicMock()
        mock_controller.export.return_value = (
            Path("/dst/id_ed25519"),
            Path("/dst/id_ed25519.pub"),
        )
        mocker.patch(
            "mvmctl.api.key_operations.KeyController",
            return_value=mock_controller,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")
        mocker.patch(
            "mvmctl.api.key_operations.CacheUtils.get_keys_dir",
            return_value=Path("/keys"),
        )

        result = KeyOperation.export(
            KeyInput(name=["export-me"]),
            Path("/dst"),
        )

        assert len(result.item) == 2
        mock_controller.export.assert_called_once_with(
            destination=Path("/dst"), keys_dir=Path("/keys"), overwrite=False
        )

    def test_export_raises_on_multiple(self, mocker):
        """export() raises when multiple keys match."""
        mock_resolved = mocker.MagicMock()
        mock_resolved.keys = [_make_key("k1"), _make_key("k2")]
        mock_request = mocker.MagicMock()
        mock_request.resolve.return_value = mock_resolved
        mocker.patch(
            "mvmctl.api.key_operations.KeyRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.key_operations.KeyRepository")

        result = KeyOperation.export(KeyInput(name=["amb"]), Path("/dst"))
        assert result.status == "error"
        assert "Expected exactly one" in result.message
