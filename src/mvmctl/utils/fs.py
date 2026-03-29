"""Filesystem path helpers for MVM cache directories."""

import os
from pathlib import Path

from mvmctl.constants import PROJECT_NAME, env_var
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
        under_home = resolved.is_relative_to(home)
        under_tmp = (os.getuid() != 0) and resolved.is_relative_to(tmp)
        if not (under_home or under_tmp):
            raise MVMError(
                f"Unsafe {env_var('CACHE_DIR')} path '{override}': "
                f"must be under $HOME ({home}) or /tmp"
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
        under_home = resolved.is_relative_to(home)
        under_tmp = (os.getuid() != 0) and resolved.is_relative_to(tmp)
        if not (under_home or under_tmp):
            raise MVMError(
                f"Unsafe {env_var('CONFIG_DIR')} path '{override}': "
                f"must be under $HOME ({home}) or /tmp"
            )
        return resolved
    return _get_real_home() / ".config" / PROJECT_NAME


def get_config_file() -> Path:
    """Return the path to the MVM config file (config.json)."""
    return get_config_dir() / "config.json"


def get_vms_dir() -> Path:
    """Return the directory that holds VM state and per-VM dirs."""
    return get_cache_dir() / "vms"


def get_vm_dir(name: str) -> Path:
    """Return the directory for a specific VM."""
    return get_vms_dir() / name


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
    """Return the directory for SSH key management."""
    # Keys are runtime assets and belong under cache dir
    return get_cache_dir() / "keys"


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


def chown_to_real_user(path: Path) -> None:
    """Recursively chown *path* to the real invoking user when running under sudo.

    This corrects ownership of cache/config files created as root so the
    non-root user can access them after ``sudo mvm host init``.
    No-op when not running under sudo or when the path does not exist.
    """
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
