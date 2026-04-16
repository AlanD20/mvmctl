"""SSH key management using database storage.

This module handles SSH key creation, import, export, and removal.
"""

from __future__ import annotations

import base64
import hashlib
import logging
import shutil
import socket
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.core._internal._db import Database
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.db.models import SSHKey
from mvmctl.exceptions import MVMKeyError
from mvmctl.utils.fs import get_config_dir

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)


class KeyController:
    """Manages SSH key lifecycle operations for a specific key.

    This class handles SSH key creation, import, export, and removal.

    Args:
        key: Key name, ID prefix, or SSHKey db model instance.
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
            resolver = KeyResolver(self._db)
            self._key = resolver.resolve(entity)

    @property
    def key_id(self) -> str:
        """Get the resolved key ID (fingerprint)."""
        return self._key.id

    @property
    def key_name(self) -> str:
        """Get the resolved key name."""
        return self._key.name

    @staticmethod
    def _compute_fingerprint(pub_key_content: str) -> str:
        """Compute SHA256 fingerprint from public key content."""
        parts = pub_key_content.strip().split()
        if len(parts) < 2:
            raise MVMKeyError("Invalid public key format")
        key_bytes = base64.b64decode(parts[1])
        digest = hashlib.sha256(key_bytes).digest()
        fp = base64.b64encode(digest).rstrip(b"=").decode()
        return f"SHA256:{fp}"

    @staticmethod
    def _parse_algorithm(pub_key_content: str) -> str:
        """Extract algorithm from public key content."""
        parts = pub_key_content.strip().split()
        if not parts:
            raise MVMKeyError("Invalid public key format")
        return parts[0]

    @staticmethod
    def _parse_comment(pub_key_content: str) -> str:
        """Extract comment from public key content."""
        parts = pub_key_content.strip().split(None, 2)
        if len(parts) >= 3:
            return parts[2]
        return ""

    @staticmethod
    def _is_private_key(content: str) -> bool:
        """Check if content contains a PEM-encoded private key header."""
        return "-----BEGIN" in content and "PRIVATE KEY-----" in content

    @staticmethod
    def _get_keys_config_dir() -> Path:
        """Return the directory for SSH key management (in config dir)."""
        return get_config_dir() / "keys"

    def _generate_keypair(self, private_key_path: Path, pub_key_path: Path, comment: str) -> str:
        """Run ssh-keygen to create an ED25519 keypair."""
        cmd = [
            "ssh-keygen",
            "-t",
            "ed25519",
            "-f",
            str(private_key_path),
            "-N",
            "",
            "-C",
            comment,
        ]

        result = subprocess.run(cmd, capture_output=True, text=True)
        if result.returncode != 0:
            raise MVMKeyError(f"ssh-keygen failed: {result.stderr.strip()}")

        return pub_key_path.read_text().strip()

    def _persist_public_key(self, name: str, pub_key_content: str) -> Path:
        """Write a public key to disk in the keys directory."""
        keys_dir = self._get_keys_config_dir()
        keys_dir.mkdir(parents=True, exist_ok=True)
        pub_path = keys_dir / f"{name}.pub"
        pub_path.write_text(pub_key_content + "\n")
        return pub_path

    @staticmethod
    def _read_pubkey_file(path: Path) -> str:
        """Read public key content from a file path."""
        if not path.exists():
            raise MVMKeyError(f"Public key file not found: {path}")

        try:
            content = path.read_text().strip()
            if not content:
                raise MVMKeyError(f"Public key file is empty: {path}")
            return content
        except OSError as e:
            raise MVMKeyError(f"Failed to read public key file: {e}") from e

    @staticmethod
    def get_pubkey(key_name: str, db: Database | None = None) -> str:
        """Get the public key content for a key by name."""
        db = db if db is not None else Database()
        repo = KeyRepository(db)

        ssh_key = repo.get_by_name(key_name)
        if ssh_key is None:
            raise MVMKeyError(f"Key '{key_name}' not found in cache")

        if ssh_key.public_key_path is None:
            raise MVMKeyError(f"Key '{ssh_key.name}' has no public key path")

        return KeyController._read_pubkey_file(Path(ssh_key.public_key_path)).strip()

    @staticmethod
    def get_pubkeys(key_names: list[str], db: Database | None = None) -> list[str]:
        """Get public key contents for multiple keys by name."""
        contents: list[str] = []
        for name in key_names:
            contents.append(KeyController.get_pubkey(name, db))
        return contents

    def inspect(self) -> SSHKey:
        """Return the SSHKey model for this key."""
        return self._key

    def add_key(self, name: str, pub_key_path: str | Path, overwrite: bool = False) -> SSHKey:
        """Add a public key to the cache."""
        pub_key_path = Path(pub_key_path)
        if not pub_key_path.exists():
            raise MVMKeyError(f"Public key file not found: {pub_key_path}")

        content = pub_key_path.read_text().strip()
        if not content:
            raise MVMKeyError(f"Public key file is empty: {pub_key_path}")

        if self._is_private_key(content):
            pub_path = Path(str(pub_key_path) + ".pub")
            if pub_path.exists():
                raise MVMKeyError(
                    f"'{pub_key_path}' looks like a private key.\n"
                    f"Use the public key instead: mvm key add {name} {pub_path}"
                )
            raise MVMKeyError(
                f"'{pub_key_path}' looks like a private key.\n"
                f"Pass the corresponding .pub file instead: mvm key add {name} <path>.pub"
            )

        existing = self._repo.get_by_name(name)
        if existing is not None:
            if overwrite:
                old_pub = self._get_keys_config_dir() / f"{name}.pub"
                if old_pub.exists():
                    old_pub.unlink()
                self._repo.delete(existing.id)
            else:
                raise MVMKeyError(f"Key '{name}' already exists. Remove it first to replace.")

        self._persist_public_key(name, content)

        private_key_path = pub_key_path.with_suffix("")
        if private_key_path == pub_key_path:
            private_key_path = Path(str(pub_key_path).replace(".pub", ""))
        private_key_exists = private_key_path.exists() and private_key_path != pub_key_path

        fingerprint = self._compute_fingerprint(content)
        now = datetime.now(timezone.utc).isoformat()
        ssh_key = SSHKey(
            id=fingerprint,
            name=name,
            fingerprint=fingerprint,
            algorithm=self._parse_algorithm(content),
            comment=self._parse_comment(content),
            private_key_path=str(private_key_path) if private_key_exists else None,
            public_key_path=str(self._get_keys_config_dir() / f"{name}.pub"),
            is_default=False,
            created_at=now,
            updated_at=now,
        )
        self._repo.upsert(ssh_key)

        return ssh_key

    def create_key(
        self,
        name: str,
        output_dir: str | Path | None = None,
        comment: str | None = None,
        is_default: bool = False,
        overwrite: bool = False,
    ) -> tuple[SSHKey, Path]:
        """Generate a new ED25519 keypair."""
        if output_dir is None:
            output_dir = self._get_keys_config_dir()
        output_dir = Path(output_dir)
        output_dir.mkdir(parents=True, exist_ok=True)

        private_key_path = output_dir / name
        pub_key_path = output_dir / f"{name}.pub"

        if not overwrite and (private_key_path.exists() or pub_key_path.exists()):
            existing = private_key_path if private_key_path.exists() else pub_key_path
            raise MVMKeyError(f"Key file already exists: {existing}. Use --overwrite to replace.")

        existing_key = self._repo.get_by_name(name)
        if existing_key is not None:
            if overwrite:
                self._repo.delete(existing_key.id)
            else:
                raise MVMKeyError(f"Key '{name}' already exists in cache. Remove it first.")

        if comment is None:
            comment = f"{name}@{socket.gethostname()}"

        if overwrite:
            if private_key_path.exists():
                private_key_path.unlink()
            if pub_key_path.exists():
                pub_key_path.unlink()

        content = self._generate_keypair(private_key_path, pub_key_path, comment)
        self._persist_public_key(name, content)

        fingerprint = self._compute_fingerprint(content)
        now = datetime.now(timezone.utc).isoformat()
        ssh_key = SSHKey(
            id=fingerprint,
            name=name,
            fingerprint=fingerprint,
            algorithm=self._parse_algorithm(content),
            comment=self._parse_comment(content),
            private_key_path=str(private_key_path),
            public_key_path=str(pub_key_path),
            is_default=is_default,
            created_at=now,
            updated_at=now,
        )
        self._repo.upsert(ssh_key)

        return ssh_key, private_key_path

    def remove(self) -> None:
        """Remove the resolved key from the cache."""
        self._repo.delete(self._key.id)

        pub_file = self._get_keys_config_dir() / f"{self._key.name}.pub"
        if pub_file.exists():
            pub_file.unlink()

    def export(
        self, destination: str | Path | None = None, overwrite: bool = False
    ) -> tuple[Path, Path]:
        """Export the keypair to a destination directory."""
        keys_dir = self._get_keys_config_dir()
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

    @staticmethod
    def set_default_keys(names: list[str], db: Database | None = None) -> None:
        """Set the default SSH keys list used when creating VMs without --ssh-key."""
        db = db if db is not None else Database()
        repo = KeyRepository(db)

        all_keys = repo.list_all()
        name_to_key = {k.name: k for k in all_keys}

        for name in names:
            if name not in name_to_key:
                raise MVMKeyError(
                    f"Key '{name}' not found in cache. "
                    "Add it first with 'mvm key add' or 'mvm key create'."
                )

        for name in names:
            if name in name_to_key:
                repo.set_default(name_to_key[name].id)

        logger.info("Set default SSH keys: %s", names)

    @staticmethod
    def get_default_keys(db: Database | None = None) -> list[str]:
        """Get the list of default SSH key names."""
        db = db if db is not None else Database()
        repo = KeyRepository(db)
        default_key = repo.get_default()
        return [default_key.name] if default_key else []

    @staticmethod
    def clear_default_keys(db: Database | None = None) -> None:
        """Clear all default SSH keys."""
        db = db if db is not None else Database()
        with db.connect() as conn:
            conn.execute("UPDATE ssh_keys SET is_default = 0")
        logger.info("Cleared default SSH keys")

    @staticmethod
    def list_keys(db: Database | None = None) -> list[SSHKey]:
        """List all keys in the cache."""
        db = db if db is not None else Database()
        repo = KeyRepository(db)
        return repo.list_all()


__all__ = [
    "KeyController",
]
