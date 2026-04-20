import fcntl
import importlib.resources
import os
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.exceptions import MVMError

if TYPE_CHECKING:
    from importlib.resources.abc import Traversable


def get_assets_dir() -> Path:
    """Return the path to the bundled assets directory inside the package."""
    return Path(__file__).parent.parent / "assets"


def get_asset_file(*path_parts: str) -> "Traversable":
    """Return a traversable path to a bundled asset file.

    Uses ``importlib.resources`` for reliable access to package resources,
    which works regardless of how the package is installed (regular install,
    zipped, or PyInstaller bundled).

    Supports nested paths by passing multiple path components or using
    path separators within a single string.

    Args:
        *path_parts: Path components to the asset file. Can be a single
            filename (e.g., "cloud-init.template.yaml") or multiple
            components for nested paths (e.g., "templates", "cloud-init.yaml").

    Returns:
        A traversable path to the asset file.

    Example::

        # Simple file in assets root
        template_path = get_asset_file("cloud-init.template.yaml")

        # Nested file using multiple arguments
        template_path = get_asset_file("templates", "cloud-init.yaml")

        # Nested file using path separator
        config_path = get_asset_file("configs/defaults.yaml")

        # Read file contents
        content = template_path.read_text()
    """
    base = importlib.resources.files("mvmctl.assets")
    for part in path_parts:
        base = base.joinpath(part)
    return base


def read_asset_file(filename: str) -> str:
    """Read and return the contents of a bundled asset file as text.

    Args:
        filename: Name of the asset file (e.g., "cloud-init.template.yaml").

    Returns:
        Contents of the asset file as a string.

    Raises:
        MVMError: If the asset file cannot be read.

    Example::

        template_content = read_asset_file("cloud-init.template.yaml")
    """
    try:
        return get_asset_file(filename).read_text()
    except (OSError, ValueError) as exc:
        raise MVMError(
            f"Failed to read asset file '{filename}': {exc}"
        ) from exc


def get_real_user_ids() -> tuple[int, int] | None:
    """Return (uid, gid) of the real invoking user when running under sudo.

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


def write_pid_file(pid_file: Path, pid: int, mode: int = 0o600) -> None:
    fd = os.open(str(pid_file), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        os.write(fd, str(pid).encode())
    finally:
        fcntl.flock(fd, fcntl.LOCK_UN)
        os.close(fd)


def read_pid_file(pid_file: Path) -> int | None:
    if not pid_file.exists():
        return None
    try:
        pid = int(pid_file.read_text().strip())
    except (ValueError, OSError):
        return None
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return None
    except PermissionError:
        pass
    return pid


def write_exit_code(vm_dir: Path, exit_code: int, filename: str) -> None:
    try:
        (vm_dir / filename).write_text(str(exit_code))
    except OSError:
        pass


def secure_mkdir(directory: Path, name: str) -> None:
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


def chown_to_real_user(path: Path) -> None:
    ids = get_real_user_ids()
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


def is_file_missing(path: Path | None) -> bool:
    """Check if a file is missing or None."""
    if path is None:
        return True
    return not path.exists()


def get_file_size(path: Path | None, fallback: int = 0) -> int:
    if path and path.exists():
        return path.stat().st_size
    return fallback
