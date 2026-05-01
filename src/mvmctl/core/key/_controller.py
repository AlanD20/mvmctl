"""
SSH key management using database storage.

This module handles SSH key lifecycle operations for a specific key instance.
For stateless key operations, use KeyService.
"""

from __future__ import annotations

import shutil
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.models import SSHKeyItem

if TYPE_CHECKING:
    pass


class KeyController:
    """
    Manages SSH key lifecycle operations for a specific key.

    This class handles SSH key operations bound to a specific key instance.
    For stateless operations (creating new keys, listing all keys, etc.),
    use KeyService instead.

    Args:
        entity: Key name, ID prefix, or SSHKeyItem db model instance.
        db: Optional Database instance (creates new if None).

    Raises:
        KeyNotFoundError: If the key cannot be resolved.

    """

    def __init__(self, entity: str | SSHKeyItem, repo: KeyRepository) -> None:
        self._repo = repo

        if isinstance(entity, SSHKeyItem):
            self._key = entity
        else:
            self._resolver = KeyResolver(self._repo)
            self._key = self._resolver.resolve(entity)

    @property
    def key_id(self) -> str:
        """Get the resolved key ID (fingerprint)."""
        return self._key.id

    @property
    def key_name(self) -> str:
        """Get the resolved key name."""
        return self._key.name

    def inspect(self) -> SSHKeyItem:
        """Return the SSHKeyItem model for this key."""
        return self._key

    def remove(self) -> None:
        """
        Remove the resolved key from database only.

        File cleanup is handled by the orchestration layer.
        """
        self._repo.delete(self._key.id)

    def export(
        self, destination: Path, *, keys_dir: Path, overwrite: bool = False
    ) -> tuple[Path, Path]:
        """
        Export the keypair to a destination directory.

        Args:
            destination: Destination directory (required, must be explicit).
            keys_dir: Directory where source key files are stored.
            overwrite: If True, overwrite existing files.

        Raises:
            KeyExportError: If source key files not found or destination files exist.

        """
        from mvmctl.exceptions import KeyExportError

        source_private = keys_dir / self._key.name
        source_public = keys_dir / f"{self._key.name}.pub"

        if not source_private.exists():
            raise KeyExportError(
                f"Private key not found for '{self._key.name}'"
            )
        if not source_public.exists():
            raise KeyExportError(f"Public key not found for '{self._key.name}'")

        destination.mkdir(parents=True, exist_ok=True)

        dest_private = destination / self._key.name
        dest_public = destination / f"{self._key.name}.pub"

        if not overwrite:
            existing = []
            if dest_private.exists():
                existing.append(str(dest_private))
            if dest_public.exists():
                existing.append(str(dest_public))
            if existing:
                raise KeyExportError(
                    f"Key file(s) already exist: {', '.join(existing)}. "
                    "Use --overwrite to replace."
                )

        shutil.copy2(source_private, dest_private)
        shutil.copy2(source_public, dest_public)
        dest_private.chmod(CONST_FILE_PERMS_PRIVATE_KEY)

        return dest_private, dest_public


__all__ = [
    "KeyController",
]
