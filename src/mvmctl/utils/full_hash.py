"""Full hash generation for database primary keys.

All assets (images, kernels, binaries, networks, VMs) use 64-character SHA256
hashes as their primary keys in the database.

Hash generation is deterministic given the same inputs, with timestamps used
to ensure uniqueness when content alone is ambiguous.

Located in utils/ because hash generation is a pure utility function used
across multiple layers.
"""

from __future__ import annotations

import hashlib
from pathlib import Path


def generate_full_hash_image(
    file_path: Path,
    os_slug: str,
    timestamp: str,
) -> str:
    """Generate 64-character SHA256 hash for an image.

    Hash includes:
    - File content hash (SHA256 of file bytes)
    - OS slug (e.g., "alpine-3.21")
    - Timestamp for uniqueness

    Args:
        file_path: Path to the image file on disk.
        os_slug: Short OS identifier (e.g., "ubuntu-24.04").
        timestamp: ISO format timestamp string for uniqueness.

    Returns:
        64-character lowercase hexadecimal SHA256 hash.
    """
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{os_slug}:{timestamp}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_kernel(
    file_path: Path,
    version: str,
    arch: str,
) -> str:
    """Generate 64-character SHA256 hash for a kernel.

    Hash includes:
    - File content hash
    - Version string (e.g., "6.1.102")
    - Architecture (e.g., "x86_64")

    Args:
        file_path: Path to the kernel file on disk.
        version: Kernel version string.
        arch: Target architecture.

    Returns:
        64-character lowercase hexadecimal SHA256 hash.
    """
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{version}:{arch}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_binary(
    file_path: Path,
    name: str,
    version: str,
) -> str:
    """Generate 64-character SHA256 hash for a binary.

    Hash includes:
    - File content hash
    - Binary name (e.g., "firecracker")
    - Version string (e.g., "1.15.0")

    Args:
        file_path: Path to the binary file on disk.
        name: Binary name.
        version: Binary version string.

    Returns:
        64-character lowercase hexadecimal SHA256 hash.
    """
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{name}:{version}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_vm(
    name: str,
    image_id: str,
    kernel_id: str,
    created_at: str,
) -> str:
    """Generate 64-character SHA256 hash for a VM.

    VM hash is content-addressed based on:
    - VM name
    - Image ID (full 64-char hash)
    - Kernel ID (full 64-char hash)
    - Creation timestamp (ISO format)

    Args:
        name: VM name.
        image_id: Full 64-character SHA256 hash of the image.
        kernel_id: Full 64-character SHA256 hash of the kernel.
        created_at: ISO format creation timestamp.

    Returns:
        64-character lowercase hexadecimal SHA256 hash.
    """
    data = f"{name}:{image_id}:{kernel_id}:{created_at}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_network(
    name: str,
    subnet: str,
    created_at: str,
) -> str:
    """Generate 64-character SHA256 hash for a network.

    Args:
        name: Network name (e.g., "default").
        subnet: Network CIDR (e.g., "172.35.0.0/24").
        created_at: ISO format creation timestamp.

    Returns:
        64-character lowercase hexadecimal SHA256 hash.
    """
    data = f"{name}:{subnet}:{created_at}"
    return hashlib.sha256(data.encode()).hexdigest()


def shorten_hash(full_hash: str, length: int = 12) -> str:
    """Return the first N characters of a full hash for UI display.

    The database always stores the full 64-character hash. This function
    returns the shortened version used in CLI output.

    Args:
        full_hash: Full 64-character SHA256 hash.
        length: Number of characters to display (default: 12).

    Returns:
        First `length` characters of the hash.

    Raises:
        ValueError: If full_hash is shorter than length.
    """
    if len(full_hash) < length:
        raise ValueError(f"Hash '{full_hash}' is shorter than requested length {length}")
    return full_hash[:length]
