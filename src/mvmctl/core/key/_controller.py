"""SSH key management using database storage.

This module handles SSH key lifecycle operations for a specific key instance.
For stateless key operations, use KeyService.
"""

from __future__ import annotations

import shutil
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.core._internal._db import Database
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.key._service import KeyService
from mvmctl.db.models import SSHKey
from mvmctl.exceptions import MVMKeyError

if TYPE_CHECKING:
    pass


class KeyController:
    """Manages SSH key lifecycle operations for a specific key.

    This class handles SSH key operations bound to a specific key instance.
    For stateless operations (creating new keys, listing all keys, etc.),
    use KeyService instead.

    Args:
        entity: Key name, ID prefix, or SSHKey db model instance.
        db: Optional Database instance (creates new if None).

    Raises:
        KeyNotFoundError: If the key cannot be resolved.
    """

    def __init__(self, entity: str | SSHKey, db: Database | None = None) -> None:
        self._db = db if db is not None else Database()
        self._repo = KeyRepository(self._db)

        if isinstance(entity, SSHKey):
            self._key = entity
        else:
            resolver = KeyResolver(self._repo)
            self._key = resolver.resolve(entity)

    @property
    def key_id(self) -> str:
        """Get the resolved key ID (fingerprint)."""
        return self._key.id

    @property
    def key_name(self) -> str:
        """Get the resolved key name."""
        return self._key.name

    def inspect(self) -> SSHKey:
        """Return the SSHKey model for this key."""
        return self._key

    def remove(self) -> None:
        """Remove the resolved key from the cache."""
        self._repo.delete(self._key.id)

        pub_file = KeyService._get_keys_config_dir() / f"{self._key.name}.pub"
        if pub_file.exists():
            pub_file.unlink()

    def export(
        self, destination: str | Path | None = None, overwrite: bool = False
    ) -> tuple[Path, Path]:
        """Export the keypair to a destination directory."""
        keys_dir = KeyService._get_keys_config_dir()
        source_private = keys_dir / self._key.name
        source_public = keys_dir / f"{self._key.name}.pub"

        if not source_private.exists():
            raise MVMKeyError(
                f"Private key '{self._key.name}' not found in cache at {source_private}"
            )
        if not source_public.exists():
            raise MVMKeyError(
                f"Public key '{self._key.name}.pub' not found in cache at {source_public}"
            )

        if destination is None:
            destination = Path.home() / ".ssh"
        destination = Path(destination)
        destination.mkdir(parents=True, exist_ok=True)

        dest_private = destination / self._key.name
        dest_public = destination / f"{self._key.name}.pub"

        if not overwrite:
            existing_files = []
            if dest_private.exists():
                existing_files.append(str(dest_private))
            if dest_public.exists():
                existing_files.append(str(dest_public))
            if existing_files:
                raise MVMKeyError(
                    f"Key file(s) already exist at destination: {', '.join(existing_files)}. "
                    "Use --overwrite to replace."
                )

        shutil.copy2(source_private, dest_private)
        shutil.copy2(source_public, dest_public)
        dest_private.chmod(CONST_FILE_PERMS_PRIVATE_KEY)

        return dest_private, dest_public


__all__ = [
    "KeyController",
]
