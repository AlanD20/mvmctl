"""Common utilities — domain-agnostic helpers reused across all layers."""

from __future__ import annotations

import json
import os
import re
import tempfile
from pathlib import Path
from typing import Any, overload

from mvmctl.constants import CLI_NAME, CONST_DIR_PERMS_CACHE, DEBUG_MODE
from mvmctl.exceptions import MVMError

# Shell metacharacters that must be rejected from user input
_SHELL_METACHARACTERS = set(";|&$`\\\"'\n\r\t<>{}[]()")

# Path traversal characters
_PATH_TRAVERSAL_CHARS = set("./~\\")

# Null byte and control characters (0-31, 127)
_CONTROL_CHARS = set(chr(i) for i in range(32)) | {chr(127)}

# Zero-width characters (Unicode)
_ZERO_WIDTH_CHARS = set("\u200b\u200c\u200d\ufeff")

# Combined dangerous characters for defense-in-depth checks
_DANGEROUS_CHARS = (
    _SHELL_METACHARACTERS
    | _PATH_TRAVERSAL_CHARS
    | _CONTROL_CHARS
    | _ZERO_WIDTH_CHARS
)

# Reserved names that cannot be used as entity names
_RESERVED_NAMES = frozenset(
    {
        "help",
        "all",
        "default",
        "none",
        "root",
        "self",
        "system",
        "true",
        "false",
        "yes",
        "no",
        "on",
        "off",
        "nil",
        "null",
    }
)

# DNS label limit — used as default max length for entity names
_MAX_NAME_LENGTH = 63

# Pattern for valid entity names: starts with alphanumeric, then allows
# alphanumeric, dot, hyphen, underscore. After first char, allows 62 more
# (for max total length of 63).
_NAME_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]{0,62}$")

# ---------------------------------------------------------------------------
# Debug state
# ---------------------------------------------------------------------------

_debug_mode: bool = DEBUG_MODE


def set_debug_mode(value: bool) -> None:
    """Set the debug mode state. Called once from main.py CLI callback."""
    global _debug_mode
    _debug_mode = value


def is_debug_mode() -> bool:
    """Return whether debug mode is currently enabled."""
    return _debug_mode


# ---------------------------------------------------------------------------
# Environment variable access
# ---------------------------------------------------------------------------


class env:
    """Centralizes all ``MVM_*`` environment variable access.

    Provides static methods for getting, setting, and constructing
    environment variable names with the standard CLI prefix.
    """

    _prefix: str = f"{CLI_NAME.upper()}_"

    @staticmethod
    def key(suffix: str) -> str:
        """Return the full env var name (e.g. ``CACHE_DIR`` → ``MVM_CACHE_DIR``)."""
        return f"{env._prefix}{suffix}"

    @staticmethod
    @overload
    def get(suffix: str) -> str | None: ...

    @staticmethod
    @overload
    def get(suffix: str, default: str) -> str: ...

    @staticmethod
    def get(suffix: str, default: str | None = None) -> str | None:
        """Read the environment variable for *suffix*, falling back to *default*."""
        return os.environ.get(env.key(suffix), default)

    @staticmethod
    def set(suffix: str, value: str) -> None:
        """Set the environment variable for *suffix* to *value*."""
        os.environ[env.key(suffix)] = value


def _get_real_home() -> Path:
    """
    Return the real user's home directory.

    When running under ``sudo``, ``SUDO_USER`` is set to the invoking user.
    Use that user's home so that state files are written to the invoking
    user's cache dir rather than root's.
    """
    import os
    import pwd

    sudo_user = os.environ.get("SUDO_USER")
    if sudo_user:
        try:
            return Path(pwd.getpwnam(sudo_user).pw_dir)
        except KeyError:
            pass
    return Path.home()


class CacheUtils:
    """
    Shared cache/temp directory utilities for VM images and ready pools.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def resolve_dir(path: Path) -> Path:
        """
        Ensure a directory exists, creating parents if necessary.

        When running under ``sudo`` the directory is chowned to the
        invoking user so it remains accessible after privileges are dropped.

        Args:
            path: Directory path to resolve.

        Returns:
            The resolved directory path.

        """
        path.mkdir(parents=True, exist_ok=True, mode=CONST_DIR_PERMS_CACHE)
        # Chown to the real user when running under sudo so that cache
        # directories are accessible after privileges are dropped.
        from mvmctl.utils.fs import FsUtils as _FsUtils

        _FsUtils.chown_to_real_user(path)
        return path

    @staticmethod
    def get_warm_image_dir(tmp_path: Path | None = None) -> Path:
        """
        Get the tmpfs ready pool directory for fast clones.

        This is the directory where decompressed images are cached for
        fast reflink copies during VM creation.

        Args:
            tmp_path: Optional override for the base temp directory.
                      If None, uses tempfile.gettempdir().

        Returns:
            Path to the warm image directory (e.g. /dev/shm/mvmctl/ready).

        """
        from mvmctl.constants import PROJECT_NAME

        base = tmp_path if tmp_path is not None else Path(tempfile.gettempdir())
        cache_dir = Path(base / PROJECT_NAME / "ready")
        return CacheUtils.resolve_dir(cache_dir)

    @staticmethod
    def get_cache_dir() -> Path:
        """
        Return the MVM cache root directory.

        Checks MVM_CACHE_DIR env var first, then falls back to
        ~/.cache/<project-name>.  When running under sudo, uses the invoking
        user's home directory so state is shared with the non-root user.
        """
        from mvmctl.constants import PROJECT_NAME
        from mvmctl.exceptions import MVMError

        override = env.get("CACHE_DIR")
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
                    f"Unsafe {env.key('CACHE_DIR')} path '{override}': "
                    f"must be under $HOME ({home}), /tmp, or /var/tmp"
                )
            return resolved
        return _get_real_home() / ".cache" / str(PROJECT_NAME)

    @staticmethod
    def get_config_dir() -> Path:
        """
        Return the MVM config directory.

        Checks MVM_CONFIG_DIR env var first, then falls back to
        ~/.config/<project-name>.
        """
        from mvmctl.constants import PROJECT_NAME
        from mvmctl.exceptions import MVMError

        override = env.get("CONFIG_DIR")
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
                    f"Unsafe {env.key('CONFIG_DIR')} path '{override}': "
                    f"must be under $HOME ({home}), /tmp, or /var/tmp"
                )
            return resolved
        return _get_real_home() / ".config" / str(PROJECT_NAME)

    @staticmethod
    def get_config_path() -> Path:
        """Return the path to the MVM config file (config.json)."""
        return CacheUtils.get_config_dir() / "config.json"

    @staticmethod
    def get_mvm_db_path() -> Path:
        """
        Return the path to the SQLite database file.

        The database lives in the MVM cache directory as ``mvmdb.db``.
        """
        from mvmctl.constants import MVM_DB_FILENAME

        return CacheUtils.get_cache_dir() / MVM_DB_FILENAME

    @staticmethod
    def get_temp_dir() -> Path:
        """Return the temp directory for microVMs."""
        from mvmctl.constants import PROJECT_NAME

        override = env.get("TEMP_DIR")
        result = Path(override) if override else Path("/tmp") / PROJECT_NAME
        result.mkdir(parents=True, exist_ok=True)
        return result

    @staticmethod
    def get_vms_dir() -> Path:
        """Return the directory that holds VM state and per-VM dirs."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "vms")

    @staticmethod
    def get_vm_dir(id: str) -> Path:
        """
        Return the directory path for a specific VM by its hash.

        Does NOT create the directory — callers must create it explicitly
        via FsUtils.secure_mkdir() when appropriate.
        """
        return CacheUtils.get_vms_dir() / id

    @staticmethod
    def get_images_dir() -> Path:
        """Return the directory for cached images."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "images")

    @staticmethod
    def get_kernels_dir() -> Path:
        """Return the directory for cached kernels."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "kernels")

    @staticmethod
    def get_keys_dir() -> Path:
        """Return the directory for SSH key management (in config dir)."""
        return CacheUtils.resolve_dir(CacheUtils.get_config_dir() / "keys")

    @staticmethod
    def get_volumes_dir() -> Path:
        """Return the directory for persistent volumes."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "volumes")

    @staticmethod
    def get_bin_dir() -> Path:
        """Return the directory for cached Firecracker binaries."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "bin")

    @staticmethod
    def get_logs_dir() -> Path:
        """Return the directory for VM and process log files."""
        return CacheUtils.resolve_dir(CacheUtils.get_cache_dir() / "logs")

    @staticmethod
    def get_audit_log_path() -> Path:
        """Return the path to the audit log file."""
        return CacheUtils.get_cache_dir() / "audit.log"

    @staticmethod
    def get_timing_log_path() -> Path:
        """Return the path to the timing log file."""
        return CacheUtils.get_cache_dir() / "timing.log"

    @staticmethod
    def get_log_path() -> Path:
        """Return the path to the log file."""
        return CacheUtils.get_cache_dir() / "mvmctl.log"


class CommonUtils:
    """
    Domain-agnostic utilities reused across VM, network, image, kernel, key, etc.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def contains_dangerous_chars(value: str) -> bool:
        """
        Check if value contains shell metacharacters, path traversal, control chars,
        or zero-width characters.

        Args:
            value: String to check.

        Returns:
            True if any dangerous character is found.

        """
        return any(c in _DANGEROUS_CHARS for c in value)

    @staticmethod
    def is_reserved_name(name: str) -> bool:
        """
        Check if name is a reserved keyword.

        Args:
            name: Name to check.

        Returns:
            True if name is reserved.

        """
        return name.lower() in _RESERVED_NAMES

    @staticmethod
    def validate_entity_name(
        name: str,
        entity_type: str = "entity",
        max_length: int = _MAX_NAME_LENGTH,
    ) -> str:
        """
        Validate any entity name (VM, network, image, kernel, key, binary).

        Applies defense-in-depth validation:
        1. Rejects empty names
        2. Checks for dangerous characters (shell metachar, path traversal, control, zero-width)
        3. Rejects reserved names
        4. Rejects names starting with hyphen
        5. Rejects IP-like names
        6. Enforces pattern and length

        Args:
            name: Entity name to validate.
            entity_type: Label for error messages (e.g. "VM", "network").
            max_length: Maximum allowed characters (default 63, matches DNS label limit).

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.

        """
        if not name:
            raise MVMError(f"Invalid {entity_type} name: cannot be empty")

        if len(name) > max_length:
            raise MVMError(
                f"Invalid {entity_type} name '{name}': exceeds maximum length "
                f"of {max_length} characters"
            )

        if name.startswith("-"):
            raise MVMError(
                f"Invalid {entity_type} name '{name}': cannot start with a hyphen"
            )

        if CommonUtils.is_reserved_name(name):
            raise MVMError(
                f"Invalid {entity_type} name '{name}': '{name}' is a reserved name"
            )

        if CommonUtils.contains_dangerous_chars(name):
            raise MVMError(
                f"Invalid {entity_type} name '{name}': contains forbidden characters "
                "(shell metacharacters, path traversal, or control characters)"
            )

        # Import here to avoid circular dependency at module level
        from mvmctl.utils._validators import NetworkValidator

        if NetworkValidator.is_ip_address(name):
            raise MVMError(
                f"Invalid {entity_type} name '{name}': cannot be an IP address"
            )

        if not _NAME_PATTERN.match(name):
            raise MVMError(
                f"Invalid {entity_type} name '{name}': must match "
                r"^[a-z0-9][a-z0-9._-]{0,62}$"
            )

        return name

    @staticmethod
    def sanitize_for_log(value: str) -> str:
        """
        Strip CRLF and control characters for safe embedding in audit logs.

        Args:
            value: String to sanitize.

        Returns:
            Sanitized string safe for audit log detail field.

        """
        return "".join(
            c
            for c in value
            if c not in _CONTROL_CHARS and c not in _ZERO_WIDTH_CHARS
        )

    @staticmethod
    def human_readable_datetime(iso_timestamp: str | None) -> str:
        """
        Format ISO timestamp to 'YYYY/MM/DD HH:MM:SS'.

        Args:
            iso_timestamp: ISO format timestamp string (e.g. from datetime.now().isoformat()).

        Returns:
            Formatted string 'YYYY/MM/DD HH:MM:SS', or "-" if input is empty/None.

        """
        from datetime import datetime

        if not iso_timestamp:
            return "-"
        try:
            dt = datetime.fromisoformat(
                str(iso_timestamp).replace("Z", "+00:00")
            )
            return dt.strftime("%Y/%m/%d %H:%M:%S")
        except (ValueError, AttributeError):
            return str(iso_timestamp)

    @staticmethod
    def format_bytes_human_readable(size_bytes: int) -> str:
        """Format bytes using binary (IEC) units — e.g. "512 B", "4.2 MiB", "1.5 GiB"."""
        if size_bytes < 1024:
            return f"{size_bytes} B"
        size_float = float(size_bytes)
        for unit in ["KiB", "MiB", "GiB"]:
            size_float /= 1024
            if size_float < 1024:
                return f"{size_float:.1f} {unit}"
        return f"{size_float:.1f} TiB"

    @staticmethod
    def generate_batch_names(base_name: str, count: int) -> list[str]:
        """Generate unique names for batch VM creation.

        First VM keeps base name, subsequent VMs get -N suffix.

        Args:
            base_name: Base name for the VM.
            count: Number of VMs to generate names for.

        Returns:
            List of unique VM names. When count=1, returns ``[base_name]``.
            When count>1, returns ``[base_name, base_name-2, base_name-3, ...]``.

        Example:
            >>> CommonUtils.generate_batch_names("my-vm", 3)
            ['my-vm', 'my-vm-2', 'my-vm-3']
        """
        if count == 1:
            return [base_name]
        names = [base_name]
        for i in range(2, count + 1):
            names.append(f"{base_name}-{i}")
        return names

    @staticmethod
    def coerce_bool_fields(instance: object, field_names: set[str]) -> None:
        """Coerce specified fields to Python bool.

        SQLite returns integers (0/1) for boolean columns. This coerces
        them to proper Python bool values. Idempotent for already-correct
        bool values.

        Args:
            instance: Dataclass instance with the fields.
            field_names: Set of field names to coerce.

        """
        for name in field_names:
            value = getattr(instance, name)
            if not isinstance(value, bool):
                setattr(instance, name, bool(value))

    @staticmethod
    def coerce(value: Any, expected_type: type) -> Any:
        """
        Coerce a value to the expected type.

        Handles string→bool, string→int, string→float, string→dict via json.loads.
        Raises TypeError if coercion fails.

        Args:
            value: Value to coerce.
            expected_type: Target Python type.

        Returns:
            Coerced value.

        Raises:
            TypeError: If value cannot be coerced to expected_type.

        """
        if expected_type is bool and isinstance(value, str):
            return value.lower() in ("true", "1", "yes", "on")
        if expected_type is int and isinstance(value, str):
            return int(value)
        if expected_type is float and isinstance(value, str):
            return float(value)
        if expected_type is dict and isinstance(value, str):
            return json.loads(value)
        if not isinstance(value, expected_type):
            raise TypeError(
                f"Expected {expected_type.__name__}, got {type(value).__name__}"
            )
        return value

    @staticmethod
    def deep_merge_dict(
        base: dict[str, Any], override: dict[str, Any]
    ) -> dict[str, Any]:
        """Recursively deep-merge override into base, returning a new dict.

        Merge rules:
        - For each key in *override*: if the value is a dict and *base* also
          has a dict for that key, recurse into both.
        - If either side is not a dict, the *override* value wins.
        - Lists: the *override* value wins entirely (standard override).
          If list merging is needed (e.g. union for kvm_capabilities),
          do it explicitly at the call site after the merge.

        Args:
            base: Base dictionary to merge into.
            override: Override dictionary whose values take precedence.

        Returns:
            New merged dictionary (neither input is mutated).

        Example:
            >>> deep_merge_dict({"a": {"b": 1}}, {"a": {"c": 2}})
            {"a": {"b": 1, "c": 2}}

        """
        result = base.copy()
        for key, override_val in override.items():
            if (
                key in result
                and isinstance(result[key], dict)
                and isinstance(override_val, dict)
            ):
                result[key] = CommonUtils.deep_merge_dict(
                    result[key], override_val
                )
            else:
                result[key] = override_val
        return result

    @staticmethod
    def safe_int(value: object, default: int = 0) -> int:
        """
        Safely extract an integer from a value.

        Args:
            value: The value to convert (int, float, str, or other).
            default: Default to return if conversion fails.

        Returns:
            The integer value, or default if conversion fails.

        """
        if isinstance(value, int):
            return value
        if isinstance(value, float):
            return int(value)
        if isinstance(value, str):
            try:
                return int(value)
            except ValueError:
                return default
        return default
