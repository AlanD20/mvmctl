"""Disk size parsing utilities.

Uses IEC binary units (KiB, MiB, GiB) where:
- K/KB = KiB = 1024 bytes
- M/MB = MiB = 1024² bytes
- G/GB = GiB = 1024³ bytes
- T/TB = TiB = 1024⁴ bytes

Note: The CLI accepts "M"/"MB" but treats both as MiB (binary),
which is the industry standard for disk and memory sizing.
"""

import re
from typing import Final

from mvmctl.exceptions import MVMError

# Size multipliers (IEC binary units: KiB, MiB, GiB, TiB)
_SIZE_MULTIPLIERS: Final[dict[str, int]] = {
    "B": 1,
    "K": 1024,
    "KB": 1024,
    "M": 1024**2,
    "MB": 1024**2,
    "G": 1024**3,
    "GB": 1024**3,
    "T": 1024**4,
    "TB": 1024**4,
}

_SIZE_PATTERN: Final[re.Pattern[str]] = re.compile(
    r"^(\d+(?:\.\d+)?)\s*([KMGT]?B?|[kmgt]?b?)?$"
)


def parse_disk_size(size_str: str) -> int:
    """Parse disk size string to bytes.

    Supports: 512M, 1G, 2.5GB, 1024K, etc.
    Case-insensitive. Whitespace allowed between number and unit.

    Args:
        size_str: Size string like "512M", "1G", "2.5GB"

    Returns:
        Size in bytes as integer

    Raises:
        MVMError: If format is invalid
    """
    size_str = size_str.strip().upper()
    match = _SIZE_PATTERN.match(size_str)

    if not match:
        raise MVMError(
            f"Invalid disk size format: '{size_str}'. "
            "Expected format: <number><unit> where unit is B, K, KB, M, MB, G, GB, T, TB"
        )

    number_str, unit = match.groups()
    unit = unit or "B"  # Default to bytes if no unit

    try:
        number = float(number_str)
    except ValueError:
        raise MVMError(f"Invalid number in disk size: '{number_str}'")

    multiplier = _SIZE_MULTIPLIERS.get(unit.upper())
    if multiplier is None:
        raise MVMError(
            f"Unknown size unit: '{unit}'. Valid: B, K, KB, M, MB, G, GB, T, TB"
        )

    bytes_count = int(number * multiplier)

    if bytes_count < 0:
        raise MVMError(f"Disk size cannot be negative: {size_str}")

    return bytes_count


def format_sectors_human_readable(
    size_sectors: int, sector_size: int = 512
) -> str:
    """Convert size in sectors to human-readable format (MiB/GiB).

    Args:
        size_sectors: Size in sectors
        sector_size: Sector size in bytes (default 512)

    Returns:
        Human-readable string like "512.0 MiB", "2.5 GiB"
    """
    size_bytes = size_sectors * sector_size
    size_mib = size_bytes / (1024 * 1024)
    if size_mib >= 1024:
        return f"{size_mib / 1024:.1f} GiB"
    return f"{size_mib:.1f} MiB"


def format_disk_size(bytes_count: int) -> str:
    """Format bytes to human-readable string.

    Args:
        bytes_count: Size in bytes

    Returns:
        Human-readable string like "1.5G", "512M"
    """
    for unit, multiplier in sorted(
        _SIZE_MULTIPLIERS.items(), key=lambda x: x[1], reverse=True
    ):
        if unit in ("B", "KB", "MB", "GB", "TB"):  # Skip short forms for output
            continue
        if bytes_count >= multiplier:
            value = bytes_count / multiplier
            if value == int(value):
                return f"{int(value)}{unit}"
            return f"{value:.1f}{unit}"
    return f"{bytes_count}B"
