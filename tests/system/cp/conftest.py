"""CP domain system tests — fixtures and helpers."""

from __future__ import annotations

from pathlib import Path


def _make_test_file(tmp_path: Path, name: str, content: str) -> Path:
    """Create a temporary test file with the given name and content.

    Returns:
        The path to the created file.
    """
    file_path = tmp_path / name
    file_path.write_text(content)
    return file_path
