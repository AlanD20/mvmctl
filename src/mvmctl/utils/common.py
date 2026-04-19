"""Common utilities — domain-agnostic helpers reused across all layers."""

from __future__ import annotations

import re

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


class CommonUtils:
    """Domain-agnostic utilities reused across VM, network, image, kernel, key, etc.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def contains_dangerous_chars(value: str) -> bool:
        """Check if value contains shell metacharacters, path traversal, control chars,
        or zero-width characters.

        Args:
            value: String to check.

        Returns:
            True if any dangerous character is found.
        """
        return any(c in _DANGEROUS_CHARS for c in value)

    @staticmethod
    def is_reserved_name(name: str) -> bool:
        """Check if name is a reserved keyword.

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
        """Validate any entity name (VM, network, image, kernel, key, binary).

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
        from mvmctl.utils._network_validator import NetworkValidator

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
        """Strip CRLF and control characters for safe embedding in audit logs.

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
        """Format ISO timestamp to 'YYYY/MM/DD HH:MM:SS'.

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
