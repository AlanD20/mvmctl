"""Config API - wraps core config functions with DB resolution."""

from typing import Any

from mvmctl.api.metadata import (
    get_default_binary_entry,
    get_default_image_entry,
    get_default_kernel_entry,
)
from mvmctl.core.config import dump_config, load_config, validate_config
from mvmctl.core.config_state import (
    get_defaults_config as _core_get_defaults_config,
)
from mvmctl.core.config_state import (
    get_firecracker_config as _core_get_firecracker_config,
)
from mvmctl.core.config_state import (
    initialize_default_config,
)
from mvmctl.core.config_state import (
    set_defaults_value as _core_set_defaults_value,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.user_config import get_config_value, get_full_user_config, set_config_value
from mvmctl.models.config import SystemDefaultsConfig


def get_firecracker_config() -> dict[str, str]:
    """Get Firecracker binary configuration with DB resolution.

    Queries MVMDatabase for the default binary and passes it to core.

    Returns:
        Dictionary with Firecracker version and path information.
    """
    db = MVMDatabase()
    binary_record = db.get_default_binary("firecracker")
    return _core_get_firecracker_config(binary_record)


def get_defaults_config() -> dict[str, Any]:
    """Get default image and kernel configuration with DB resolution.

    Queries MVMDatabase for defaults and passes explicit values to core.

    Returns:
        Dictionary with 'image' and 'kernel' keys containing default values.
    """
    db = MVMDatabase()

    # Query DB for image default
    default_image_slug: str | None = None
    try:
        images = db.list_images()
        for image in images:
            if image.is_default:
                default_image_slug = image.os_slug or image.id
                break
    except Exception:
        pass

    # Query DB for kernel default
    default_kernel_path: str | None = None
    try:
        kernels = db.list_kernels()
        for kernel in kernels:
            if kernel.is_default:
                default_kernel_path = kernel.path
                break
    except Exception:
        pass

    return _core_get_defaults_config(
        default_image_slug=default_image_slug,
        default_kernel_path=default_kernel_path,
    )


def set_defaults_value(key: str, value: Any) -> None:
    """Set a default value with DB update.

    Updates both JSON metadata and SQLite database.

    Args:
        key: The configuration key to set.
        value: The value to set.
    """
    db = MVMDatabase()
    _core_set_defaults_value(key, value, db)


__all__ = [
    "dump_config",
    "load_config",
    "validate_config",
    "get_config_value",
    "set_config_value",
    "get_full_user_config",
    "get_firecracker_config",
    "get_defaults_config",
    "set_defaults_value",
    "initialize_default_config",
    "get_default_image_entry",
    "get_default_kernel_entry",
    "get_default_binary_entry",
    "SystemDefaultsConfig",
]
