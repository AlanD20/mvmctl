"""
SSH key service - stateless key operations.

This module provides stateless SSH key operations that don't require
a specific key entity to be bound.
"""

from __future__ import annotations

import base64
import hashlib
import logging
import shutil
import socket
from datetime import UTC, datetime
from pathlib import Path

from mvmctl.core.key._repository import KeyRepository
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import SSHKeyItem
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)


class KeyService:
    """Stateless SSH key operations service."""

    def __init__(self, repo: KeyRepository) -> None:
        self._repo = repo

    @classmethod
    def check_dependencies(cls) -> None:
        """
        Check that ssh-keygen is available.

        Raises:
            KeyDependencyError: If ssh-keygen is not found in PATH.

        """
        from mvmctl.exceptions import KeyDependencyError

        if shutil.which("ssh-keygen") is None:
            raise KeyDependencyError(
                "ssh-keygen not found in PATH. "
                "Install OpenSSH client package (e.g., 'apt install openssh-client')."
            )

    def _compute_fingerprint(self, pub_key_content: str) -> str:
        """Compute SHA256 fingerprint from public key content."""
        parts = pub_key_content.strip().split()
        if len(parts) < 2:
            raise MVMKeyError("Invalid public key format")
        key_bytes = base64.b64decode(parts[1])
        digest = hashlib.sha256(key_bytes).digest()
        fp = base64.b64encode(digest).rstrip(b"=").decode()
        return f"SHA256:{fp}"

    def _parse_algorithm(self, pub_key_content: str) -> str:
        """Extract algorithm from public key content."""
        parts = pub_key_content.strip().split()
        if not parts:
            raise MVMKeyError("Invalid public key format")
        return parts[0]

    def _parse_comment(self, pub_key_content: str) -> str:
        """Extract comment from public key content."""
        parts = pub_key_content.strip().split(None, 2)
        if len(parts) >= 3:
            return parts[2]
        return ""

    def _is_private_key(self, content: str) -> bool:
        """Check if content contains a PEM-encoded private key header."""
        return "-----BEGIN" in content and "PRIVATE KEY-----" in content

    def _generate_keypair(
        self,
        private_key_path: Path,
        pub_key_path: Path,
        comment: str,
        algorithm: str = "ed25519",
        bits: int | None = None,
    ) -> str:
        """Run ssh-keygen to create a keypair."""
        cmd = [
            "ssh-keygen",
            "-t",
            algorithm,
            "-f",
            str(private_key_path),
            "-N",
            "",
            "-C",
            comment,
        ]
        if algorithm == "rsa":
            cmd.extend(["-b", str(bits or 4096)])

        result = run_cmd(cmd, check=False)
        if result.returncode != 0:
            raise MVMKeyError(f"ssh-keygen failed: {result.stderr.strip()}")

        return pub_key_path.read_text().strip()

    def _persist_public_key(
        self, name: str, pub_key_content: str, keys_dir: Path
    ) -> Path:
        """Write a public key to disk."""
        from mvmctl.exceptions import KeyFileError

        keys_dir.mkdir(parents=True, exist_ok=True)
        pub_path = keys_dir / f"{name}.pub"
        try:
            pub_path.write_text(pub_key_content + "\n")
        except OSError as e:
            raise KeyFileError(f"Failed to write public key file: {e}") from e
        return pub_path

    def _read_pubkey_file(self, path: Path) -> str:
        """Read public key content from a file path."""
        from mvmctl.exceptions import KeyFileError

        if not path.exists():
            raise KeyFileError(f"Public key file not found: {path}")

        try:
            content = path.read_text().strip()
            if not content:
                raise KeyFileError(f"Public key file is empty: {path}")
            return content
        except OSError as e:
            raise KeyFileError(f"Failed to read public key file: {e}") from e

    def get_pubkey(self, key: str | SSHKeyItem, keys_dir: Path) -> str:
        """Get the public key content for a key by name."""
        if isinstance(key, SSHKeyItem):
            ssh_key = key
        else:
            found = self._repo.get_by_name(key)
            if found is None:
                raise MVMKeyError(f"Key '{key}' not found in cache")
            ssh_key = found

        pub_path = keys_dir / f"{ssh_key.name}.pub"
        return self._read_pubkey_file(pub_path).strip()

    def get_pubkeys(
        self, keys: list[str] | list[SSHKeyItem], keys_dir: Path
    ) -> list[str]:
        """Get public key contents for multiple keys by name."""
        contents: list[str] = []
        for name in keys:
            contents.append(self.get_pubkey(name, keys_dir))
        return contents

    def set_default_keys(self, names: list[str]) -> None:
        """Set the default SSH keys.

        Notes:
            Key name existence validation is handled by the API layer
            (KeyRequest.resolve()) before this method is called.

        """
        all_keys = self._repo.list_all()
        name_to_key = {k.name: k for k in all_keys}

        for name in names:
            if name in name_to_key:
                self._repo.set_default(name_to_key[name].id)

        logger.info("Set default SSH keys: %s", names)

    def clear_default_keys(self) -> None:
        """Clear all default SSH keys using the repository."""
        self._repo.clear_defaults()
        logger.info("Cleared default SSH keys")

    def list_keys(
        self, keys_dir: Path, *, verify: bool = True
    ) -> list[SSHKeyItem]:
        """
        List all keys in the cache, syncing is_present with filesystem.

        Args:
            keys_dir: Directory where key files are stored.
            verify: If True (default), check filesystem and update DB.
                   If False, return DB records as-is.

        """
        keys = self._repo.list_all()
        if not verify:
            return keys

        missing_ids: list[str] = []
        for key in keys:
            pub_path = keys_dir / f"{key.name}.pub"
            if not pub_path.exists():
                missing_ids.append(key.id)

        if missing_ids:
            self._repo.update_many_is_present(missing_ids, False)
            keys = self._repo.list_all()

        return keys

    def create_keypair(
        self,
        name: str,
        output_dir: Path,
        *,
        algorithm: str = "ed25519",
        bits: int | None = None,
        comment: str | None = None,
        is_default: bool = False,
        overwrite: bool = False,
    ) -> tuple[SSHKeyItem, Path]:
        """
        Generate a new SSH keypair.

        Args:
            name: Key name.
            output_dir: Directory to write key files (required).
            algorithm: Key algorithm for ssh-keygen.
            bits: Key size in bits (RSA only; defaults to 4096).
            comment: Key comment.
            is_default: Whether to set as default.
            overwrite: Whether to overwrite existing files.

        Returns:
            Tuple of (SSHKeyItem, private_key_path).

        Notes:
            File conflict validation is handled by the API layer before
            this method is called. This method handles DB duplicate
            detection (state) and overwrite file cleanup (execution).

        """
        self.check_dependencies()

        output_dir = Path(output_dir)
        output_dir.mkdir(parents=True, exist_ok=True)

        private_key_path = output_dir / name
        pub_key_path = output_dir / f"{name}.pub"

        existing_key = self._repo.get_by_name(name)
        if existing_key is not None:
            if overwrite:
                self._repo.delete(existing_key.id)
            else:
                raise MVMKeyError(
                    f"Key '{name}' already exists in cache. Remove it first."
                )

        if comment is None:
            comment = f"{name}@{socket.gethostname()}"

        if overwrite:
            if private_key_path.exists():
                private_key_path.unlink()
            if pub_key_path.exists():
                pub_key_path.unlink()

        content = self._generate_keypair(
            private_key_path, pub_key_path, comment, algorithm, bits
        )
        self._persist_public_key(name, content, output_dir)

        fingerprint = self._compute_fingerprint(content)
        now = datetime.now(UTC).isoformat()
        ssh_key = SSHKeyItem(
            id=fingerprint,
            name=name,
            fingerprint=fingerprint,
            algorithm=self._parse_algorithm(content),
            comment=self._parse_comment(content),
            private_key_path=str(private_key_path),
            public_key_path=str(pub_key_path),
            is_default=is_default,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        self._repo.upsert(ssh_key)

        return ssh_key, private_key_path

    def add_key(
        self,
        name: str,
        pub_key_path: Path,
        pub_key_content: str,
        keys_dir: Path,
        *,
        overwrite: bool = False,
    ) -> SSHKeyItem:
        """
        Add a public key to the cache.

        Args:
            name: Key name.
            pub_key_path: Original public key file path (for metadata).
            pub_key_content: Pre-read and validated public key content.
            keys_dir: Directory to copy the public key into.
            overwrite: Whether to overwrite existing.

        Notes:
            File format validation (path existence, empty content, private-key
            detection) is handled by the API layer before this method is called.

        """
        existing = self._repo.get_by_name(name)
        if existing is not None:
            if overwrite:
                old_pub = keys_dir / f"{name}.pub"
                if old_pub.exists():
                    old_pub.unlink()
                self._repo.delete(existing.id)
            else:
                raise MVMKeyError(
                    f"Key '{name}' already exists. Remove it first to replace."
                )

        self._persist_public_key(name, pub_key_content, keys_dir)

        private_key_path = pub_key_path.with_suffix("")
        if private_key_path == pub_key_path:
            private_key_path = Path(str(pub_key_path).replace(".pub", ""))
        private_key_exists = (
            private_key_path.exists() and private_key_path != pub_key_path
        )

        fingerprint = self._compute_fingerprint(pub_key_content)
        now = datetime.now(UTC).isoformat()
        ssh_key = SSHKeyItem(
            id=fingerprint,
            name=name,
            fingerprint=fingerprint,
            algorithm=self._parse_algorithm(pub_key_content),
            comment=self._parse_comment(pub_key_content),
            private_key_path=str(private_key_path)
            if private_key_exists
            else None,
            public_key_path=str(keys_dir / f"{name}.pub"),
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        self._repo.upsert(ssh_key)

        return ssh_key


__all__ = [
    "KeyService",
]
