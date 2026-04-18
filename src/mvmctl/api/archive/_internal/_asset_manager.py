"""Asset management - OOP implementation for bundled package assets.

This module provides a class-based interface for accessing bundled assets
(templates, YAML configs, defaults) using importlib.resources for reliable
package resource access regardless of installation method.
"""

from __future__ import annotations

import importlib.resources
from typing import TYPE_CHECKING

from mvmctl.exceptions import BundledAssetError, BundledAssetNotFoundError

if TYPE_CHECKING:
    from importlib.resources.abc import Traversable


class AssetManager:
    """Manages access to bundled package assets.

    Uses importlib.resources for reliable access to package resources,
    which works regardless of how the package is installed (regular install,
    zipped, or PyInstaller/Nuitka bundled).

    This class handles both:
    - Regular pip/uv installs (package files on filesystem)
    - PyInstaller --onefile builds (files in MEIPASS)
    - Nuitka standalone builds (files in distribution folder)

    Example::

        manager = AssetManager()

        # Get asset path and read manually
        template_path = manager.get_file("cloud-init.template.yaml")
        content = template_path.read_text()

        # Or read directly
        content = manager.read_file("firecracker.template.json")

        # Nested paths work too
        config = manager.get_file("templates", "nested.yaml")
    """

    _PACKAGE_ROOT = "mvmctl.assets"

    def __init__(self) -> None:
        """Initialize the AssetManager and verify assets are accessible."""
        try:
            self._base = importlib.resources.files(self._PACKAGE_ROOT)
        except (ImportError, ModuleNotFoundError, ValueError) as exc:
            raise BundledAssetError(
                f"Failed to access bundled assets package '{self._PACKAGE_ROOT}'. "
                f"This may indicate a corrupted installation or missing assets. "
                f"Error: {exc}"
            ) from exc

    def get_file(self, *path_parts: str) -> "Traversable":
        """Return a traversable path to a bundled asset file.

        Supports nested paths by passing multiple path components.

        Args:
            *path_parts: Path components to the asset file. Can be a single
                filename (e.g., "cloud-init.template.yaml") or multiple
                components for nested paths.

        Returns:
            A traversable path to the asset file with read_text(), read_bytes(),
            and exists() methods.

        Raises:
            BundledAssetError: If no path parts are provided.

        Example::

            # Simple file in assets root
            template = manager.get_file("cloud-init.template.yaml")

            # Nested file using multiple arguments
            template = manager.get_file("templates", "v2", "cloud-init.yaml")

            # Nested file using path separator
            config = manager.get_file("configs/defaults.yaml")
        """
        if not path_parts:
            raise BundledAssetError("At least one path part is required")

        result = self._base
        for part in path_parts:
            result = result.joinpath(part)
        return result

    def read_file(self, *path_parts: str) -> str:
        """Read and return the contents of a bundled asset file as text.

        Args:
            *path_parts: Path components to the asset file.

        Returns:
            Contents of the asset file as a string.

        Raises:
            BundledAssetNotFoundError: If the asset file does not exist.
            BundledAssetError: If the asset file cannot be read.

        Example::

            content = manager.read_file("cloud-init.template.yaml")
            nested = manager.read_file("templates", "config.yaml")
        """
        if not path_parts:
            raise BundledAssetError("At least one path part is required")

        path_str = "/".join(path_parts)

        try:
            return self.get_file(*path_parts).read_text()
        except FileNotFoundError as exc:
            raise BundledAssetNotFoundError(f"Asset file not found: '{path_str}'") from exc
        except (OSError, ValueError, PermissionError) as exc:
            raise BundledAssetError(f"Failed to read asset file '{path_str}': {exc}") from exc

    def read_bytes(self, *path_parts: str) -> bytes:
        """Read and return the contents of a bundled asset file as bytes.

        Args:
            *path_parts: Path components to the asset file.

        Returns:
            Contents of the asset file as bytes.

        Raises:
            BundledAssetNotFoundError: If the asset file does not exist.
            BundledAssetError: If the asset file cannot be read.

        Example::

            data = manager.read_bytes("images.yaml")
        """
        if not path_parts:
            raise BundledAssetError("At least one path part is required")

        path_str = "/".join(path_parts)

        try:
            return self.get_file(*path_parts).read_bytes()
        except FileNotFoundError as exc:
            raise BundledAssetNotFoundError(f"Asset file not found: '{path_str}'") from exc
        except (OSError, ValueError, PermissionError) as exc:
            raise BundledAssetError(f"Failed to read asset file '{path_str}': {exc}") from exc

    def file_exists(self, *path_parts: str) -> bool:
        """Check if a bundled asset file exists.

        Args:
            *path_parts: Path components to the asset file.

        Returns:
            True if the file exists, False otherwise.

        Example::

            if manager.file_exists("custom.template.yaml"):
                content = manager.read_file("custom.template.yaml")
        """
        if not path_parts:
            return False

        try:
            asset = self.get_file(*path_parts)
            return asset.is_file()  # type: ignore[union-attr]
        except (OSError, ValueError, AttributeError):
            return False

    def list_files(self) -> list[str]:
        """List all files in the assets root directory.

        Returns:
            List of filenames in the assets directory.

        Example::

            files = manager.list_files()
            # Returns: ['cloud-init.template.yaml', 'firecracker.template.json', ...]
        """
        try:
            return [item.name for item in self._base.iterdir() if item.is_file()]
        except (OSError, ValueError):
            return []
