"""SHA256 hash generation for all domain resources."""

from __future__ import annotations

import hashlib
import warnings
from pathlib import Path


class HashGenerator:
    """Generate content-addressed SHA256 hashes for domain resources.

    All methods return 64-character lowercase hexadecimal hashes.
    """

    @staticmethod
    def image(os_slug: str, source: str, timestamp: str) -> str:
        """Generate 64-char SHA256 hash for an image."""
        data = f"{os_slug}:{source}:{timestamp}"
        return hashlib.sha256(data.encode()).hexdigest()

    @staticmethod
    def kernel(file_path: Path, version: str, arch: str, timestamp: str) -> str:
        """Generate 64-char SHA256 hash for a kernel."""
        file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
        data = f"{file_hash}:{version}:{arch}:{timestamp}"
        return hashlib.sha256(data.encode()).hexdigest()

    @staticmethod
    def binary(file_path: Path, name: str, version: str) -> str:
        """Generate 64-char SHA256 hash for a binary."""
        file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
        data = f"{file_hash}:{name}:{version}"
        return hashlib.sha256(data.encode()).hexdigest()

    @staticmethod
    def vm(name: str, created_at: str) -> str:
        """Generate 32-char SHA256 hash for a VM.

        VM IDs are truncated to 32 characters (instead of the usual 64) so
        that filesystem paths derived from the ID stay well under the Unix
        domain socket path limit (SUN_LEN ≈108 bytes).
        """
        data = f"{name}:{created_at}"
        return hashlib.sha256(data.encode()).hexdigest()[:32]

    @staticmethod
    def network(name: str, subnet: str, created_at: str) -> str:
        """Generate 64-char SHA256 hash for a network."""
        data = f"{name}:{subnet}:{created_at}"
        return hashlib.sha256(data.encode()).hexdigest()

    @staticmethod
    def shorten(full_hash: str, length: int = 12) -> str:
        """Return first N characters of a hash for display."""
        if len(full_hash) < length:
            raise ValueError(
                f"Hash '{full_hash}' is shorter than requested length {length}"
            )
        return full_hash[:length]


# =====================================================================
# DEPRECATED — Use HashGenerator instead
# =====================================================================


def generate_full_hash_image(os_slug: str, source: str, timestamp: str) -> str:
    """Deprecated: Use HashGenerator.image()."""
    warnings.warn(
        "generate_full_hash_image is deprecated, use HashGenerator.image()",
        DeprecationWarning,
        stacklevel=2,
    )
    return HashGenerator.image(os_slug, source, timestamp)


def generate_full_hash_kernel(file_path: Path, version: str, arch: str) -> str:
    """Deprecated: Use HashGenerator.kernel()."""
    warnings.warn(
        "generate_full_hash_kernel is deprecated, use HashGenerator.kernel()",
        DeprecationWarning,
        stacklevel=2,
    )
    return HashGenerator.kernel(file_path, version, arch)


def generate_full_hash_binary(file_path: Path, name: str, version: str) -> str:
    """Deprecated: Use HashGenerator.binary()."""
    warnings.warn(
        "generate_full_hash_binary is deprecated, use HashGenerator.binary()",
        DeprecationWarning,
        stacklevel=2,
    )
    return HashGenerator.binary(file_path, name, version)


def generate_full_hash_network(name: str, subnet: str, created_at: str) -> str:
    """Deprecated: Use HashGenerator.network()."""
    warnings.warn(
        "generate_full_hash_network is deprecated, use HashGenerator.network()",
        DeprecationWarning,
        stacklevel=2,
    )
    return HashGenerator.network(name, subnet, created_at)


def shorten_hash(full_hash: str, length: int = 12) -> str:
    """Deprecated: Use HashGenerator.shorten()."""
    warnings.warn(
        "shorten_hash is deprecated, use HashGenerator.shorten()",
        DeprecationWarning,
        stacklevel=2,
    )
    return HashGenerator.shorten(full_hash, length)
