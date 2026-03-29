"""SSH key management — named key store backed by the cache folder."""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import shutil
import socket
import subprocess
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, TypedDict

from mvmctl.constants import CONST_FILE_PERMS_PRIVATE_KEY
from mvmctl.exceptions import MVMKeyError
from mvmctl.utils.fs import get_keys_dir

logger = logging.getLogger(__name__)

# Registry format sentinel keys — presence of these means we have the new wrapped format
_REGISTRY_KEYS_FIELD = "keys"
_REGISTRY_DEFAULTS_FIELD = "defaults"
_REGISTRY_SSH_DEFAULTS_FIELD = "ssh"


@dataclass
class KeyInfo:
    """Metadata for a stored keypair."""

    name: str
    fingerprint: str
    algorithm: str
    comment: str
    added_at: str
    has_private_key: bool = False
    private_key_path: str | None = None
    public_key_path: str | None = None


def _registry_path() -> Path:
    """Return the path to the key registry JSON file."""
    return get_keys_dir() / "registry.json"


def _load_registry() -> dict[str, dict[str, Any]]:
    """Load the key registry from disk, returning an empty wrapped registry if missing or corrupt.

    Returns:
        Wrapped registry dict with "keys" and "defaults" top-level fields.
    """
    path = _registry_path()
    if not path.exists():
        return {
            _REGISTRY_KEYS_FIELD: {},
            _REGISTRY_DEFAULTS_FIELD: {_REGISTRY_SSH_DEFAULTS_FIELD: []},
        }
    try:
        raw = json.loads(path.read_text())
    except (json.JSONDecodeError, ValueError):
        logger.warning("Corrupt key registry at %s  resetting to empty wrapped registry", path)
        return {
            _REGISTRY_KEYS_FIELD: {},
            _REGISTRY_DEFAULTS_FIELD: {_REGISTRY_SSH_DEFAULTS_FIELD: []},
        }

    # Expect the wrapped format only; do not perform legacy migration.
    if isinstance(raw, dict) and _REGISTRY_KEYS_FIELD in raw and _REGISTRY_DEFAULTS_FIELD in raw:
        defaults = raw.get(_REGISTRY_DEFAULTS_FIELD) or {}
        if _REGISTRY_SSH_DEFAULTS_FIELD not in defaults:
            defaults[_REGISTRY_SSH_DEFAULTS_FIELD] = []
            raw[_REGISTRY_DEFAULTS_FIELD] = defaults
        return raw

    logger.warning(
        "Unsupported key registry format at %s  found legacy or unexpected shape. Resetting to empty wrapped registry. "
        "If you need to preserve keys, please migrate manually.",
        path,
    )
    return {
        _REGISTRY_KEYS_FIELD: {},
        _REGISTRY_DEFAULTS_FIELD: {_REGISTRY_SSH_DEFAULTS_FIELD: []},
    }


def _save_registry(registry: dict[str, Any]) -> None:
    """Persist the key registry to disk with mode 0o600.

    Always writes in the new wrapped format.

    Args:
        registry: Wrapped registry dict with ``"keys"`` and ``"defaults"`` fields.
    """
    path = _registry_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(registry, indent=2))
    path.chmod(CONST_FILE_PERMS_PRIVATE_KEY)


def _compute_fingerprint(pub_key_content: str) -> str:
    """Compute SHA256 fingerprint from public key content."""
    parts = pub_key_content.strip().split()
    if len(parts) < 2:
        raise MVMKeyError("Invalid public key format")
    key_bytes = base64.b64decode(parts[1])
    digest = hashlib.sha256(key_bytes).digest()
    fp = base64.b64encode(digest).rstrip(b"=").decode()
    return f"SHA256:{fp}"


def _parse_algorithm(pub_key_content: str) -> str:
    """Extract algorithm from public key content."""
    parts = pub_key_content.strip().split()
    if not parts:
        raise MVMKeyError("Invalid public key format")
    return parts[0]


def _parse_comment(pub_key_content: str) -> str:
    """Extract comment from public key content."""
    parts = pub_key_content.strip().split(None, 2)
    if len(parts) >= 3:
        return parts[2]
    return ""


def set_default_keys(names: list[str]) -> None:
    """Set the default SSH keys list used when creating VMs without --ssh-key.

    Args:
        names: List of cached key names to set as defaults.

    Raises:
        MVMKeyError: If any name does not exist in the registry.
    """
    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    missing = [n for n in names if n not in keys]
    if missing:
        raise MVMKeyError(
            f"Key(s) not found in cache: {', '.join(missing)}. "
            "Add them first with 'mvm key add' or 'mvm key create'."
        )
    registry[_REGISTRY_DEFAULTS_FIELD][_REGISTRY_SSH_DEFAULTS_FIELD] = list(names)
    _save_registry(registry)
    logger.info("Set default SSH keys: %s", names)


def get_default_keys() -> list[str]:
    """Get the list of default SSH key names.

    Returns:
        List of cached key names that are marked as defaults (may be empty).
    """
    registry = _load_registry()
    return list(registry[_REGISTRY_DEFAULTS_FIELD][_REGISTRY_SSH_DEFAULTS_FIELD])


def clear_default_keys() -> None:
    """Clear all default SSH keys."""
    registry = _load_registry()
    registry[_REGISTRY_DEFAULTS_FIELD][_REGISTRY_SSH_DEFAULTS_FIELD] = []
    _save_registry(registry)
    logger.info("Cleared default SSH keys")


def resolve_key_input(input_str: str) -> str:
    """Resolve a key name, file path, or fingerprint to a cached key name.

    Resolution order:
    1. If ``input_str`` matches a cached key name exactly → return it.
    2. If ``input_str`` is a path to an existing ``.pub`` file → return the stem.
    3. If ``input_str`` looks like a fingerprint prefix → search by fingerprint.

    Args:
        input_str: Key name, public key file path, or fingerprint (prefix) string.

    Returns:
        Canonical cached key name.

    Raises:
        MVMKeyError: If the input cannot be resolved, or is ambiguous.
    """
    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]

    if input_str in keys:
        return input_str

    candidate = Path(input_str)
    if candidate.exists() and candidate.suffix == ".pub":
        stem = candidate.stem
        if stem in keys:
            return stem
        raise MVMKeyError(
            f"Public key file '{input_str}' found on disk but key '{stem}' is not in the cache. "
            f"Import it first with: mvm key add {stem} {input_str}"
        )

    matches = [
        name
        for name, entry in keys.items()
        if entry.get("fingerprint", "").startswith(input_str)
        or entry.get("fingerprint", "").endswith(input_str)
    ]
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        raise MVMKeyError(
            f"Ambiguous fingerprint '{input_str}' matches multiple keys: {', '.join(matches)}. "
            "Provide a longer prefix or use the key name directly."
        )

    raise MVMKeyError(
        f"Key not found: '{input_str}' is not a cached key name, "
        "a readable .pub file path, or a resolvable fingerprint."
    )


def list_keys() -> list[KeyInfo]:
    """List all keys in the cache."""
    registry = _load_registry()
    return [KeyInfo(**entry) for entry in registry[_REGISTRY_KEYS_FIELD].values()]


def get_key(name: str) -> KeyInfo | None:
    """Get a key by name, or None if not found."""
    registry = _load_registry()
    entry = registry[_REGISTRY_KEYS_FIELD].get(name)
    if entry is None:
        return None
    return KeyInfo(**entry)


def _looks_like_private_key(content: str) -> bool:
    return "BEGIN" in content and "PRIVATE KEY" in content


def add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo:
    pub_key_path = Path(pub_key_path)
    if not pub_key_path.exists():
        raise MVMKeyError(f"Public key file not found: {pub_key_path}")

    content = pub_key_path.read_text().strip()
    if not content:
        raise MVMKeyError(f"Public key file is empty: {pub_key_path}")

    if _looks_like_private_key(content):
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

    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    if name in keys:
        if overwrite:
            old_pub = get_keys_dir() / f"{name}.pub"
            if old_pub.exists():
                old_pub.unlink()
            del keys[name]
        else:
            raise MVMKeyError(f"Key '{name}' already exists. Remove it first to replace.")

    key_dir = get_keys_dir()
    key_dir.mkdir(parents=True, exist_ok=True)
    dest = key_dir / f"{name}.pub"
    dest.write_text(content + "\n")

    private_key_path = pub_key_path.with_suffix("")
    if private_key_path == pub_key_path:
        private_key_path = Path(str(pub_key_path).replace(".pub", ""))
    has_private_key = private_key_path.exists() and private_key_path != pub_key_path

    info = KeyInfo(
        name=name,
        fingerprint=_compute_fingerprint(content),
        algorithm=_parse_algorithm(content),
        comment=_parse_comment(content),
        added_at=datetime.now(timezone.utc).isoformat(),
        has_private_key=has_private_key,
        private_key_path=str(private_key_path) if has_private_key else None,
        public_key_path=str(dest),
    )
    keys[name] = asdict(info)
    _save_registry(registry)

    logger.info("Added key '%s' to cache", name)
    return info


def _generate_keypair(private_key_path: Path, pub_key_path: Path, comment: str) -> str:
    """Run ssh-keygen to create an ED25519 keypair and return the public key content.

    Args:
        private_key_path: Destination path for the private key file.
        pub_key_path: Destination path for the public key file.
        comment: Comment string embedded in the public key.

    Returns:
        The public key content as a stripped string.

    Raises:
        MVMKeyError: If ssh-keygen exits with a non-zero return code.
    """
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


def _build_key_info(
    name: str,
    pub_key_content: str,
    has_private_key: bool = False,
    private_key_path: str | None = None,
    public_key_path: str | None = None,
) -> KeyInfo:
    """Create a KeyInfo from a public key's content string.

    Args:
        name: Logical name for the key.
        pub_key_content: Raw public key text (algorithm + base64 + optional comment).
        has_private_key: Whether a private key exists for this key.
        private_key_path: Path to the private key file.
        public_key_path: Path to the public key file.

    Returns:
        A fully populated KeyInfo dataclass instance.
    """
    return KeyInfo(
        name=name,
        fingerprint=_compute_fingerprint(pub_key_content),
        algorithm=_parse_algorithm(pub_key_content),
        comment=_parse_comment(pub_key_content),
        added_at=datetime.now(timezone.utc).isoformat(),
        has_private_key=has_private_key,
        private_key_path=private_key_path,
        public_key_path=public_key_path,
    )


def _cache_public_key(name: str, pub_key_content: str) -> None:
    """Write a public key to the keys directory cache.

    Args:
        name: Logical key name (used as the filename stem).
        pub_key_content: Raw public key text to persist.
    """
    keys_dir = get_keys_dir()
    keys_dir.mkdir(parents=True, exist_ok=True)
    cache_pub = keys_dir / f"{name}.pub"
    cache_pub.write_text(pub_key_content + "\n")


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
        output_dir = get_keys_dir()
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    private_key_path = output_dir / name
    pub_key_path = output_dir / f"{name}.pub"

    if not overwrite and (private_key_path.exists() or pub_key_path.exists()):
        existing = private_key_path if private_key_path.exists() else pub_key_path
        raise MVMKeyError(f"Key file already exists: {existing}. Use --overwrite to replace.")

    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    if name in keys:
        if overwrite:
            del keys[name]
            _save_registry(registry)
        else:
            raise MVMKeyError(f"Key '{name}' already exists in cache. Remove it first.")

    if comment is None:
        comment = f"{name}@{socket.gethostname()}"

    if overwrite:
        if private_key_path.exists():
            private_key_path.unlink()
        if pub_key_path.exists():
            pub_key_path.unlink()

    content = _generate_keypair(private_key_path, pub_key_path, comment)
    _cache_public_key(name, content)
    info = _build_key_info(
        name,
        content,
        has_private_key=True,
        private_key_path=str(private_key_path),
        public_key_path=str(pub_key_path),
    )

    registry[_REGISTRY_KEYS_FIELD][name] = asdict(info)
    _save_registry(registry)

    logger.info("Created key '%s', private key at %s", name, private_key_path)
    return info, private_key_path


def remove_key(name: str) -> None:
    """Remove a key from the cache (does not delete key files from disk)."""
    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    if name not in keys:
        raise MVMKeyError(f"Key '{name}' not found in cache")

    del keys[name]

    defaults = registry[_REGISTRY_DEFAULTS_FIELD][_REGISTRY_SSH_DEFAULTS_FIELD]
    if name in defaults:
        registry[_REGISTRY_DEFAULTS_FIELD][_REGISTRY_SSH_DEFAULTS_FIELD] = [
            n for n in defaults if n != name
        ]

    _save_registry(registry)

    pub_file = get_keys_dir() / f"{name}.pub"
    if pub_file.exists():
        pub_file.unlink()

    logger.info("Removed key '%s' from cache", name)


def export_key(
    name: str,
    destination: str | Path | None = None,
    overwrite: bool = False,
) -> tuple[Path, Path]:
    """Export a keypair from cache to destination directory.

    Args:
        name: Key name in the cache
        destination: Destination directory (default: ~/.ssh/)
        overwrite: Whether to overwrite existing files

    Returns:
        Tuple of (private_key_path, public_key_path) at destination

    Raises:
        MVMKeyError: If key not found in cache, or if files exist and overwrite=False
    """
    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    if name not in keys:
        raise MVMKeyError(f"Key '{name}' not found in cache")

    keys_dir = get_keys_dir()
    source_private = keys_dir / name
    source_public = keys_dir / f"{name}.pub"

    if not source_private.exists():
        raise MVMKeyError(f"Private key '{name}' not found in cache at {source_private}")
    if not source_public.exists():
        raise MVMKeyError(f"Public key '{name}.pub' not found in cache at {source_public}")

    if destination is None:
        destination = Path.home() / ".ssh"
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)

    dest_private = destination / name
    dest_public = destination / f"{name}.pub"

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

    keys[name]["private_key_path"] = str(dest_private)
    keys[name]["public_key_path"] = str(dest_public)
    _save_registry(registry)

    logger.info("Exported key '%s' to %s", name, destination)
    return dest_private, dest_public


class KeyInspect(TypedDict):
    name: str
    fingerprint: str
    algorithm: str
    comment: str
    added_at: str
    public_key: str
    has_private_key: bool
    private_key_path: str | None
    public_key_path: str | None


def inspect_key(name: str) -> KeyInspect:
    """Return detailed info about a named key."""
    registry = _load_registry()
    keys = registry[_REGISTRY_KEYS_FIELD]
    if name not in keys:
        raise MVMKeyError(f"Key '{name}' not found in cache")

    entry = keys[name]
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
        has_private_key=entry.get("has_private_key", False),
        private_key_path=entry.get("private_key_path"),
        public_key_path=entry.get("public_key_path"),
    )
