"""Input validation utilities for entity names and paths."""

import re

from fcm.exceptions import FCMError

_NAME_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]{0,30}$")


def validate_entity_name(name: str, entity_type: str = "entity") -> str:
    """Validate and return a safe entity name (VM, network, key).

    Args:
        name: The name to validate.
        entity_type: Label for error messages (e.g. "VM", "network", "key").

    Returns:
        The validated name.

    Raises:
        FCMError: If the name doesn't match the allowed pattern.
    """
    if not _NAME_PATTERN.match(name):
        raise FCMError(
            f"Invalid {entity_type} name '{name}': "
            f"must match [a-z0-9][a-z0-9._-]{{0,30}}"
        )
    return name


def validate_boot_arg_component(value: str, component_name: str) -> str:
    """Validate a kernel boot argument component has no injection characters.

    Args:
        value: The value to validate.
        component_name: Label for error messages.

    Returns:
        The validated value.

    Raises:
        FCMError: If the value contains spaces or shell metacharacters.
    """
    if re.search(r"[\s;|&$`\\\"']", value):
        raise FCMError(
            f"Invalid {component_name} '{value}': "
            f"must not contain spaces or shell metacharacters"
        )
    return value
