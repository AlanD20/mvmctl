"""Filesystem path helpers for FCM cache directories."""

import os
from pathlib import Path

from fcm.constants import PROJECT_NAME, env_var
from fcm.exceptions import FCMError


def get_cache_dir() -> Path:
    """Return the FCM cache root directory.

    Checks FCM_CACHE_DIR env var first, then falls back to
    ~/.cache/<project-name>.
    """
    override = os.environ.get(env_var("CACHE_DIR"))
    if override:
        resolved = Path(override).resolve()
        home = Path.home().resolve()
        tmp = Path("/tmp").resolve()
        under_home = resolved.is_relative_to(home)
        under_tmp = (os.getuid() != 0) and resolved.is_relative_to(tmp)
        if not (under_home or under_tmp):
            raise FCMError(
                f"Unsafe {env_var('CACHE_DIR')} path '{override}': "
                f"must be under $HOME ({home}) or /tmp"
            )
        return resolved
    return Path.home() / ".cache" / PROJECT_NAME


def get_config_dir() -> Path:
    """Return the FCM config directory.

    Checks FCM_CONFIG_DIR env var first, then falls back to
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
            raise FCMError(
                f"Unsafe {env_var('CONFIG_DIR')} path '{override}': "
                f"must be under $HOME ({home}) or /tmp"
            )
        return resolved
    return Path.home() / ".config" / PROJECT_NAME


def get_config_file() -> Path:
    """Return the path to the FCM config file (config.json)."""
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


def get_networks_dir() -> Path:
    """Return the directory for named network state."""
    return get_cache_dir() / "networks"


def get_network_dir(name: str) -> Path:
    """Return the directory for a specific network."""
    return get_networks_dir() / name


def get_keys_dir() -> Path:
    """Return the directory for SSH key management."""
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
