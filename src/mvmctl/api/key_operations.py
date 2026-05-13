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
from mvmctl.models.result import BatchResult, OperationResult
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
    def create(
        inputs: KeyCreateInput,
    ) -> OperationResult[SSHKeyItem]:
        """Create a new SSH keypair."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)

        try:
            service.check_dependencies()
        except Exception as e:
            return OperationResult(
                status="error",
                code="key.create_failed",
                message=str(e),
                exception=e,
            )

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
        return OperationResult(
            status="success",
            code="key.created",
            item=key_item,
        )

    @staticmethod
    def add(
        name: str, pub_key_path: Path, overwrite: bool = False
    ) -> OperationResult[SSHKeyItem]:
        """Add an existing public key to the cache."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)
        keys_dir = CacheUtils.get_keys_dir()

        try:
            # Validate file before calling service (caller validates)
            pub_key_path = Path(pub_key_path)
            if not pub_key_path.exists():
                raise MVMKeyError(f"Public key file not found: {pub_key_path}")

            pub_key_content = pub_key_path.read_text().strip()
            if not pub_key_content:
                raise MVMKeyError(f"Public key file is empty: {pub_key_path}")

            if (
                "-----BEGIN" in pub_key_content
                and "PRIVATE KEY-----" in pub_key_content
            ):
                alt_path = Path(str(pub_key_path) + ".pub")
                if alt_path.exists():
                    raise MVMKeyError(
                        f"'{pub_key_path}' looks like a private key.\n"
                        f"Use the public key instead: mvm key add {name} {alt_path}"
                    )
                raise MVMKeyError(
                    f"'{pub_key_path}' looks like a private key.\n"
                    f"Pass the corresponding .pub file instead: "
                    f"mvm key add {name} <path>.pub"
                )

            key_item = service.add_key(
                name,
                pub_key_path,
                pub_key_content,
                keys_dir,
                overwrite=overwrite,
            )
        except Exception as e:
            return OperationResult(
                status="error",
                code="key.add_failed",
                message=str(e),
                exception=e,
            )
        AuditLog.log("key.add", changes={"name": key_item.name})
        return OperationResult(
            status="success",
            code="key.added",
            item=key_item,
        )

    @staticmethod
    def remove(inputs: KeyInput) -> BatchResult[SSHKeyItem]:
        """Remove keys by name or ID."""
        db = Database()
        repo = KeyRepository(db)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()
        keys_dir = CacheUtils.get_keys_dir()

        results: list[OperationResult[SSHKeyItem]] = []
        for key in resolved.keys:
            try:
                # File cleanup is done at the API layer before DB deletion
                pub_file = keys_dir / f"{key.name}.pub"
                priv_file = keys_dir / key.name
                if pub_file.exists():
                    pub_file.unlink()
                if priv_file.exists():
                    priv_file.unlink()

                repo.delete(key.id)
                AuditLog.log("key.remove", changes={"name": key.name})
                results.append(
                    OperationResult(
                        status="success",
                        code="key.removed",
                        item=key,
                    )
                )
            except Exception as e:
                results.append(
                    OperationResult(
                        status="error",
                        code="key.remove_failed",
                        message=str(e),
                        item=key,
                        exception=e,
                    )
                )
        return BatchResult(items=results)

    @staticmethod
    def _key_to_dict(key: SSHKeyItem) -> dict[str, Any]:
        """Convert SSHKeyItem to dictionary for JSON output.

        Includes every field from the model.
        """
        return {
            "id": key.id,
            "name": key.name,
            "fingerprint": key.fingerprint,
            "algorithm": key.algorithm,
            "comment": key.comment,
            "public_key_path": key.public_key_path,
            "private_key_path": key.private_key_path,
            "is_default": key.is_default,
            "is_present": key.is_present,
            "created_at": key.created_at,
            "updated_at": key.updated_at,
        }

    @staticmethod
    def inspect(
        inputs: KeyInput, is_json: bool = False
    ) -> SSHKeyItem | dict[str, Any]:
        """Inspect a key with full details."""
        key_item = KeyOperation.get(inputs)
        if is_json:
            return KeyOperation._key_to_dict(key_item)
        return key_item

    @staticmethod
    def export(
        inputs: KeyInput,
        destination: Path,
        overwrite: bool = False,
    ) -> OperationResult[tuple[Path, Path]]:
        """Export keypair to destination directory."""
        db = Database()
        repo = KeyRepository(db)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.keys) != 1:
            return OperationResult(
                status="error",
                code="key.export_failed",
                message=f"Expected exactly one key, got {len(resolved.keys)}",
            )

        keys_dir = CacheUtils.get_keys_dir()
        controller = KeyController(resolved.keys[0], repo)
        try:
            paths = controller.export(
                destination=destination,
                keys_dir=keys_dir,
                overwrite=overwrite,
            )
        except Exception as e:
            return OperationResult(
                status="error",
                code="key.export_failed",
                message=str(e),
                exception=e,
            )
        return OperationResult(
            status="success",
            code="key.exported",
            item=paths,
        )

    @staticmethod
    def set_default(
        inputs: KeyInput,
    ) -> OperationResult[SSHKeyItem]:
        """Set keys as default."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)

        request = KeyRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        names = [k.name for k in resolved.keys]
        try:
            service.set_default_keys(names)
        except Exception as e:
            return OperationResult(
                status="error",
                code="key.default_set_failed",
                message=str(e),
                exception=e,
            )

        for name in names:
            AuditLog.log("key.set_default", changes={"name": name})

        return OperationResult(
            status="success",
            code="key.default_set",
            item=resolved.keys[0] if resolved.keys else None,
        )

    @staticmethod
    def get_defaults() -> list[SSHKeyItem]:
        """Get all default keys."""
        db = Database()
        return KeyRepository(db).get_defaults()

    @staticmethod
    def clear_defaults() -> OperationResult[None]:
        """Clear all default keys."""
        db = Database()
        repo = KeyRepository(db)
        service = KeyService(repo)
        try:
            service.clear_default_keys()
        except Exception as e:
            return OperationResult(
                status="error",
                code="key.defaults_clear_failed",
                message=str(e),
                exception=e,
            )
        AuditLog.log("key.clear_defaults")
        return OperationResult(
            status="success",
            code="key.defaults_cleared",
        )
