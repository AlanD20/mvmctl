"""Key operations - cross-domain orchestration for SSH key management."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any

from mvmctl.api.inputs._key_create_input import (
    KeyCreateInput,
    KeyCreateRequest,
)
from mvmctl.api.inputs._key_input import (
    KeyInput,
    KeyRequest,
)
from mvmctl.core._shared import Database
from mvmctl.core.key._controller import KeyController
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._service import KeyService
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import SSHKeyItem
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.common import CacheUtils

logger = logging.getLogger(__name__)

__all__ = ["KeyOperation"]


class KeyOperation:
    """Orchestration layer for SSH key operations."""

    @staticmethod
    def list_all() -> list[SSHKeyItem]:
        """List all SSH keys."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)
        keys_dir = CacheUtils.get_keys_dir()
        return service.list_keys(keys_dir)

    @staticmethod
    def get(inputs: KeyInput) -> SSHKeyItem:
        """Get a single key by name or ID."""
        db = Database()
        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()
        if len(resolved.keys) != 1:
            raise MVMKeyError(
                f"Expected exactly one key, got {len(resolved.keys)}"
            )
        return resolved.keys[0]

    @staticmethod
    def create(inputs: KeyCreateInput) -> SSHKeyItem:
        """Create a new SSH keypair. Returns SSHKeyItem."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)

        service.check_dependencies()

        request = KeyCreateRequest(inputs=inputs)
        resolved = request.resolve()

        key_item = service.create_keypair(
            name=resolved.name,
            output_dir=resolved.output_dir,
            algorithm=resolved.algorithm,
            bits=resolved.bits,
            comment=resolved.comment,
            is_default=resolved.set_default,
            overwrite=resolved.overwrite,
        )[0]

        AuditLog.log(
            "key.create",
            changes={"name": key_item.name, "algorithm": key_item.algorithm},
        )
        return key_item

    @staticmethod
    def add(
        name: str, pub_key_path: Path, overwrite: bool = False
    ) -> SSHKeyItem:
        """Add an existing public key to the cache."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)
        keys_dir = CacheUtils.get_keys_dir()
        key_item = service.add_key(
            name, pub_key_path, keys_dir, overwrite=overwrite
        )
        AuditLog.log("key.add", changes={"name": key_item.name})
        return key_item

    @staticmethod
    def remove(inputs: KeyInput) -> None:
        """Remove keys by name or ID."""
        db = Database()
        repo = KeyRepository(db)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()
        keys_dir = CacheUtils.get_keys_dir()

        for key in resolved.keys:
            # File cleanup is done at the API layer before DB deletion
            pub_file = keys_dir / f"{key.name}.pub"
            priv_file = keys_dir / key.name
            if pub_file.exists():
                pub_file.unlink()
            if priv_file.exists():
                priv_file.unlink()

            controller = KeyController(key, repo)
            controller.remove()
            AuditLog.log("key.remove", changes={"name": key.name})

    @staticmethod
    def inspect(
        inputs: KeyInput, is_json: bool = False
    ) -> SSHKeyItem | dict[str, Any]:
        """Inspect a key with full details."""
        key_item = KeyOperation.get(inputs)
        if is_json:
            return {
                "id": key_item.id,
                "name": key_item.name,
                "fingerprint": key_item.fingerprint,
                "algorithm": key_item.algorithm,
                "comment": key_item.comment,
                "public_key_path": key_item.public_key_path,
                "private_key_path": key_item.private_key_path,
                "is_default": key_item.is_default,
                "created_at": key_item.created_at,
            }
        return key_item

    @staticmethod
    def export(
        inputs: KeyInput,
        destination: Path,
        overwrite: bool = False,
    ) -> tuple[Path, Path]:
        """Export keypair to destination directory."""
        db = Database()
        repo = KeyRepository(db)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.keys) != 1:
            raise MVMKeyError(
                f"Expected exactly one key, got {len(resolved.keys)}"
            )

        keys_dir = CacheUtils.get_keys_dir()
        controller = KeyController(resolved.keys[0], repo)
        return controller.export(
            destination=destination, keys_dir=keys_dir, overwrite=overwrite
        )

    @staticmethod
    def set_default(inputs: KeyInput) -> None:
        """Set keys as default."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        names = [k.name for k in resolved.keys]
        service.set_default_keys(names)

        for name in names:
            AuditLog.log("key.set_default", changes={"name": name})

    @staticmethod
    def get_defaults() -> list[SSHKeyItem]:
        """Get all default keys."""
        db = Database()
        return KeyRepository(db).get_defaults()

    @staticmethod
    def clear_defaults() -> None:
        """Clear all default keys."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)
        service.clear_default_keys()
        AuditLog.log("key.clear_defaults")
