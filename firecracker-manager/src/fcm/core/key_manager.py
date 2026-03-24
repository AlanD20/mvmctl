"""SSH key management — named key store backed by the cache folder."""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import socket
import subprocess
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, TypedDict

from fcm.exceptions import FCMKeyError
from fcm.utils.fs import get_keys_dir

logger = logging.getLogger(__name__)


@dataclass
class KeyInfo:
    """Metadata for a stored public key."""

    name: str
    fingerprint: str
    algorithm: str
    comment: str
    added_at: str


def _registry_path() -> Path:
    """Return the path to the key registry JSON file."""
    return get_keys_dir() / "registry.json"


def _load_registry() -> dict[str, dict[str, Any]]:
    """Load the key registry from disk, returning an empty dict if missing or corrupt."""
    path = _registry_path()
    if not path.exists():
        return {}
    try:
        data: dict[str, dict[str, Any]] = json.loads(path.read_text())
    except (json.JSONDecodeError, ValueError):
        logger.warning("Corrupt key registry at %s — resetting to empty", path)
        return {}
    return data


def _save_registry(registry: dict[str, dict[str, Any]]) -> None:
    """Persist the key registry to disk with mode 0o600.

    Args:
        registry: Mapping of key name to key metadata dict.
    """
    path = _registry_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(registry, indent=2))
    path.chmod(0o600)


def _compute_fingerprint(pub_key_content: str) -> str:
    """Compute SHA256 fingerprint from public key content."""
    parts = pub_key_content.strip().split()
    if len(parts) < 2:
        raise FCMKeyError("Invalid public key format")
    key_bytes = base64.b64decode(parts[1])
    digest = hashlib.sha256(key_bytes).digest()
    fp = base64.b64encode(digest).rstrip(b"=").decode()
    return f"SHA256:{fp}"


def _parse_algorithm(pub_key_content: str) -> str:
    """Extract algorithm from public key content."""
    parts = pub_key_content.strip().split()
    if not parts:
        raise FCMKeyError("Invalid public key format")
    return parts[0]


def _parse_comment(pub_key_content: str) -> str:
    """Extract comment from public key content."""
    parts = pub_key_content.strip().split(None, 2)
    if len(parts) >= 3:
        return parts[2]
    return ""


def list_keys() -> list[KeyInfo]:
    """List all keys in the cache."""
    registry = _load_registry()
    return [KeyInfo(**entry) for entry in registry.values()]


def get_key(name: str) -> KeyInfo | None:
    """Get a key by name, or None if not found."""
    registry = _load_registry()
    entry = registry.get(name)
    if entry is None:
        return None
    return KeyInfo(**entry)


def add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo:
    """Import an existing public key into the cache."""
    pub_key_path = Path(pub_key_path)
    if not pub_key_path.exists():
        raise FCMKeyError(f"Public key file not found: {pub_key_path}")

    content = pub_key_path.read_text().strip()
    if not content:
        raise FCMKeyError(f"Public key file is empty: {pub_key_path}")

    registry = _load_registry()
    if name in registry:
        if overwrite:
            # Remove old .pub file and registry entry before re-adding
            old_pub = get_keys_dir() / f"{name}.pub"
            if old_pub.exists():
                old_pub.unlink()
            del registry[name]
        else:
            raise FCMKeyError(f"Key '{name}' already exists. Remove it first to replace.")

    keys_dir = get_keys_dir()
    keys_dir.mkdir(parents=True, exist_ok=True)
    dest = keys_dir / f"{name}.pub"
    dest.write_text(content + "\n")

    info = KeyInfo(
        name=name,
        fingerprint=_compute_fingerprint(content),
        algorithm=_parse_algorithm(content),
        comment=_parse_comment(content),
        added_at=datetime.now(timezone.utc).isoformat(),
    )
    registry[name] = asdict(info)
    _save_registry(registry)

    logger.info("Added key '%s' to cache", name)
    return info


def create_key(
    name: str,
    output_dir: str | Path | None = None,
    comment: str | None = None,
    overwrite: bool = False,
) -> tuple[KeyInfo, Path]:
    """Generate a new ED25519 keypair.

    Returns (KeyInfo, private_key_path).
    """
    if output_dir is None:
        output_dir = Path.home() / ".ssh"
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    private_key_path = output_dir / name
    pub_key_path = output_dir / f"{name}.pub"

    if not overwrite and (private_key_path.exists() or pub_key_path.exists()):
        existing = private_key_path if private_key_path.exists() else pub_key_path
        raise FCMKeyError(f"Key file already exists: {existing}. Use --overwrite to replace.")

    registry = _load_registry()
    if name in registry:
        if overwrite:
            # Silently remove existing registry entry before proceeding
            del registry[name]
            _save_registry(registry)
        else:
            raise FCMKeyError(f"Key '{name}' already exists in cache. Remove it first.")

    if comment is None:
        comment = f"{name}@{socket.gethostname()}"

    if overwrite:
        if private_key_path.exists():
            private_key_path.unlink()
        if pub_key_path.exists():
            pub_key_path.unlink()

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
        raise FCMKeyError(f"ssh-keygen failed: {result.stderr.strip()}")

    # Read generated public key
    content = pub_key_path.read_text().strip()

    # Store in cache
    keys_dir = get_keys_dir()
    keys_dir.mkdir(parents=True, exist_ok=True)
    cache_pub = keys_dir / f"{name}.pub"
    cache_pub.write_text(content + "\n")

    info = KeyInfo(
        name=name,
        fingerprint=_compute_fingerprint(content),
        algorithm=_parse_algorithm(content),
        comment=_parse_comment(content),
        added_at=datetime.now(timezone.utc).isoformat(),
    )
    registry[name] = asdict(info)
    _save_registry(registry)

    logger.info("Created key '%s', private key at %s", name, private_key_path)
    return info, private_key_path


def remove_key(name: str) -> None:
    """Remove a key from the cache (does not delete key files from disk)."""
    registry = _load_registry()
    if name not in registry:
        raise FCMKeyError(f"Key '{name}' not found in cache")

    del registry[name]
    _save_registry(registry)

    # Remove cached public key file
    pub_file = get_keys_dir() / f"{name}.pub"
    if pub_file.exists():
        pub_file.unlink()

    logger.info("Removed key '%s' from cache", name)


class KeyInspect(TypedDict):
    name: str
    fingerprint: str
    algorithm: str
    comment: str
    added_at: str
    public_key: str


def inspect_key(name: str) -> KeyInspect:
    """Return detailed info about a named key."""
    registry = _load_registry()
    if name not in registry:
        raise FCMKeyError(f"Key '{name}' not found in cache")

    entry = registry[name]
    pub_file = get_keys_dir() / f"{name}.pub"
    public_key_content = ""
    if pub_file.exists():
        public_key_content = pub_file.read_text().strip()

    return KeyInspect(
        name=entry["name"],
        fingerprint=entry["fingerprint"],
        algorithm=entry["algorithm"],
        comment=entry["comment"],
        added_at=entry["added_at"],
        public_key=public_key_content,
    )
