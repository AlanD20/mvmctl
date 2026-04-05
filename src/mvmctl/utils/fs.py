import fcntl
import os
from pathlib import Path

from mvmctl.constants import MVM_DB_FILENAME, PROJECT_NAME, env_var
from mvmctl.exceptions import MVMError


def _get_real_home() -> Path:
    """Return the real user's home directory.

    When running under ``sudo``, ``SUDO_USER`` is set to the invoking user.
    Use that user's home so that state files are written to the invoking
    user's cache dir rather than root's.
    """
    sudo_user = os.environ.get("SUDO_USER")
    if sudo_user:
        import pwd

        try:
            return Path(pwd.getpwnam(sudo_user).pw_dir)
        except KeyError:
            pass
    return Path.home()


def get_cache_dir() -> Path:
    """Return the MVM cache root directory.

    Checks MVM_CACHE_DIR env var first, then falls back to
    ~/.cache/<project-name>.  When running under sudo, uses the invoking
    user's home directory so state is shared with the non-root user.
    """
    override = os.environ.get(env_var("CACHE_DIR"))
    if override:
        resolved = Path(override).resolve()
        home = Path.home().resolve()
        tmp = Path("/tmp").resolve()
        var_tmp = Path("/var/tmp").resolve()
        under_home = resolved.is_relative_to(home)
        under_tmp = resolved.is_relative_to(tmp)
        under_var_tmp = resolved.is_relative_to(var_tmp)
        if not (under_home or under_tmp or under_var_tmp):
            raise MVMError(
                f"Unsafe {env_var('CACHE_DIR')} path '{override}': "
                f"must be under $HOME ({home}), /tmp, or /var/tmp"
            )
        return resolved
    return _get_real_home() / ".cache" / PROJECT_NAME


def get_config_dir() -> Path:
    """Return the MVM config directory.

    Checks MVM_CONFIG_DIR env var first, then falls back to
    ~/.config/<project-name>.
    """
    override = os.environ.get(env_var("CONFIG_DIR"))
    if override:
        resolved = Path(override).resolve()
        home = Path.home().resolve()
        tmp = Path("/tmp").resolve()
        var_tmp = Path("/var/tmp").resolve()
        under_home = resolved.is_relative_to(home)
        under_tmp = resolved.is_relative_to(tmp)
        under_var_tmp = resolved.is_relative_to(var_tmp)
        if not (under_home or under_tmp or under_var_tmp):
            raise MVMError(
                f"Unsafe {env_var('CONFIG_DIR')} path '{override}': "
                f"must be under $HOME ({home}), /tmp, or /var/tmp"
            )
        return resolved
    return _get_real_home() / ".config" / PROJECT_NAME


def get_config_file() -> Path:
    """Return the path to the MVM config file (config.json)."""
    return get_config_dir() / "config.json"


def get_mvm_db_path() -> Path:
    """Return the path to the SQLite database file.

    The database lives in the MVM cache directory as ``mvmdb.db``.

    Handles SUDO_USER correctly: when running under ``sudo``, returns
    the invoking user's cache directory (not root's), matching the
    behaviour of ``get_cache_dir()``.

    Example::

        # Returns ~/.cache/mvmctl/mvmdb.db
        # (or $MVM_CACHE_DIR/mvmdb.db if env var is set)
        path = get_mvm_db_path()
    """
    return get_cache_dir() / MVM_DB_FILENAME


def get_temp_dir() -> Path:
    override = os.environ.get(env_var("TEMP_DIR"))
    result = Path(override) if override else Path("/tmp") / PROJECT_NAME
    result.mkdir(parents=True, exist_ok=True)
    return result


def get_vms_dir() -> Path:
    """Return the directory that holds VM state and per-VM dirs."""
    return get_cache_dir() / "vms"


def get_vm_dir(name: str) -> Path:
    """Return the directory for a specific VM."""
    return get_vms_dir() / name


def get_vm_dir_by_hash(vm_hash: str) -> Path:
    """Return the directory for a specific VM by its hash."""
    return get_vms_dir() / vm_hash


def get_images_dir() -> Path:
    """Return the directory for cached images."""
    return get_cache_dir() / "images"


def get_kernels_dir() -> Path:
    """Return the directory for cached kernels."""
    return get_cache_dir() / "kernels"


def get_state_file() -> Path:
    """Return the path to the VM state JSON file."""
    return get_vms_dir() / "state.json"


def get_keys_dir() -> Path:
    """Return the directory for SSH key management (in config dir)."""
    # Keys are config data and belong under config dir
    return get_config_dir() / "keys"


def get_keys_config_dir() -> Path:
    """Return the keys directory in config (not cache).

    This is an alias for get_keys_dir() for clarity in code that
    specifically needs the config-based keys location.
    """
    return get_keys_dir()


def get_bin_dir() -> Path:
    """Return the directory for cached Firecracker binaries."""
    return get_cache_dir() / "bin"


def get_logs_dir() -> Path:
    """Return the directory for VM and process log files."""
    return get_cache_dir() / "logs"


def get_assets_dir() -> Path:
    """Return the path to the bundled assets directory inside the package."""
    return Path(__file__).parent.parent / "assets"


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
            raise MVMError(f"'{name}' path is a symlink (possible attack): {directory}")
        raise MVMError(f"'{name}' already exists at {directory}")
    except FileNotFoundError:
        pass
    try:
        directory.mkdir(parents=True, exist_ok=False)
    except FileExistsError:
        if os.path.islink(directory):
            raise MVMError(f"'{name}' path is a symlink (race condition detected): {directory}")
        raise MVMError(f"'{name}' already exists at {directory}")
    if os.path.islink(directory):
        raise MVMError(f"'{name}' directory is a symlink (security violation): {directory}")


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
