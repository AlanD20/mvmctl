"""Kernel resolution helpers."""

from __future__ import annotations

from pathlib import Path

__all__ = [
    "resolve_kernel",
]


def resolve_kernel(kernel: str | None, kernel_path: Path | None) -> tuple[Path | None, str | None]:
    """Resolve kernel from name or path.

    Args:
        kernel: Kernel name/ID or None
        kernel_path: Explicit kernel path or None

    Returns:
        Tuple of (resolved_path, resolved_id)
    """
    if kernel_path:
        return kernel_path, None

    if kernel:
        from mvmctl.core.kernel import resolve_kernel_path

        resolved = resolve_kernel_path(kernel)
        return resolved, None

    return None, None
