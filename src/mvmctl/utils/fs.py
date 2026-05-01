import fcntl
import json
import os
from pathlib import Path
from typing import Any

import yaml

from mvmctl.exceptions import MVMError


class FsUtils:
    """Filesystem utilities with symlink-attack resistant operations."""

    @staticmethod
    def _open_nofollow(path: Path) -> int:
        """Open a file with O_RDONLY, O_CLOEXEC, and O_NOFOLLOW if available."""
        flags = os.O_RDONLY | os.O_CLOEXEC
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        return os.open(str(path), flags)

    @staticmethod
    def read_json(path: Path) -> dict[str, Any] | list[Any]:
        """
        Read and parse a JSON file with O_NOFOLLOW protection.

        Args:
            path: Path to the JSON file.

        Returns:
            The parsed JSON object (dict or list).

        Raises:
            MVMError: If the file cannot be read or the JSON is invalid.

        """
        try:
            fd = FsUtils._open_nofollow(path)
        except (AttributeError, TypeError):
            fd = os.open(str(path), os.O_RDONLY | os.O_CLOEXEC)
        try:
            with os.fdopen(fd, encoding="utf-8") as f:
                result: dict[str, Any] | list[Any] = json.load(f)
                return result
        except (OSError, json.JSONDecodeError) as exc:
            raise MVMError(f"Failed to read JSON from {path}: {exc}") from exc

    @staticmethod
    def read_yaml(path: Path) -> dict[str, Any] | list[Any]:
        """
        Read and parse a YAML file with O_NOFOLLOW protection.

        Args:
            path: Path to the YAML file.

        Returns:
            The parsed YAML object (dict or list), or {} for empty files.

        Raises:
            MVMError: If the file cannot be read or the YAML is invalid.

        """
        try:
            fd = FsUtils._open_nofollow(path)
        except (AttributeError, TypeError):
            fd = os.open(str(path), os.O_RDONLY | os.O_CLOEXEC)
        try:
            with os.fdopen(fd, encoding="utf-8") as f:
                result: dict[str, Any] | list[Any] = yaml.safe_load(f)
                if result is None:
                    return {}
                return result
        except (OSError, yaml.YAMLError) as exc:
            raise MVMError(f"Failed to read YAML from {path}: {exc}") from exc

    @staticmethod
    def read_raw(path: Path) -> str:
        """
        Read the raw text content of a file with O_NOFOLLOW protection.

        Args:
            path: Path to the file.

        Returns:
            The file contents as a string.

        Raises:
            MVMError: If the file cannot be read.

        """
        try:
            fd = FsUtils._open_nofollow(path)
        except (AttributeError, TypeError):
            fd = os.open(str(path), os.O_RDONLY | os.O_CLOEXEC)
        try:
            with os.fdopen(fd, encoding="utf-8") as f:
                return f.read()
        except OSError as exc:
            raise MVMError(f"Failed to read file {path}: {exc}") from exc

    @staticmethod
    def secure_mkdir(directory: Path, name: str) -> None:
        """
        Create a directory, refusing if it or any ancestor is a symlink.

        Args:
            directory: The directory path to create.
            name: Human-readable name for error messages.

        Raises:
            MVMError: If the directory already exists or is a symlink.

        """
        try:
            os.lstat(directory)
            if os.path.islink(directory):
                raise MVMError(
                    f"'{name}' path is a symlink (possible attack): {directory}"
                )
            raise MVMError(f"'{name}' already exists at {directory}")
        except FileNotFoundError:
            pass
        try:
            directory.mkdir(parents=True, exist_ok=False)
        except FileExistsError:
            if os.path.islink(directory):
                raise MVMError(
                    f"'{name}' path is a symlink (race condition detected): {directory}"
                )
            raise MVMError(f"'{name}' already exists at {directory}")
        if os.path.islink(directory):
            raise MVMError(
                f"'{name}' directory is a symlink (security violation): {directory}"
            )

    @staticmethod
    def write_pid_file(pid_file: Path, pid: int, mode: int = 0o600) -> None:
        fd = os.open(str(pid_file), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX)
            os.write(fd, str(pid).encode())
        finally:
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)

    @staticmethod
    def get_real_user_ids() -> tuple[int, int] | None:
        """
        Return (uid, gid) of the real invoking user when running under sudo.

        Returns None if not running as root, or if SUDO_USER is not set / cannot
        be resolved — in which case no chown is needed.
        """
        if os.getuid() != 0:
            return None
        sudo_user = os.environ.get("SUDO_USER")
        if not sudo_user:
            return None
        import pwd

        try:
            pw = pwd.getpwnam(sudo_user)
            return pw.pw_uid, pw.pw_gid
        except KeyError:
            return None

    @staticmethod
    def chown_to_real_user(path: Path) -> None:
        """Recursively chown a path to the real invoking user when running under sudo."""
        ids = FsUtils.get_real_user_ids()
        if ids is None or not path.exists():
            return
        uid, gid = ids
        try:
            os.chown(path, uid, gid)
            if path.is_dir():
                for child in path.rglob("*"):
                    os.chown(child, uid, gid)
        except OSError:
            pass
