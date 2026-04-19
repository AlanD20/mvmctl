"""VM validation utilities."""

from __future__ import annotations

import re

from mvmctl.exceptions import MVMError
from mvmctl.utils.common import CommonUtils

_VALID_SSH_USERNAME = re.compile(r"^[a-z_][a-z0-9_-]*$")


class VMValidator:
    """Validate VM-specific inputs."""

    @staticmethod
    def validate_name(name: str) -> str:
        """Validate VM name.

        Args:
            name: VM name to validate.

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.
        """
        return CommonUtils.validate_entity_name(name, entity_type="VM")

    @staticmethod
    def validate_boot_arg_component(
        value: str, component_name: str = "boot arg"
    ) -> str:
        """Validate a kernel boot argument component has no injection characters.

        Args:
            value: The value to validate.
            component_name: Label for error messages.

        Returns:
            The validated value.

        Raises:
            MVMError: If the value contains spaces or shell metacharacters.
        """
        if not value:
            return value
        if re.search(r"[\s;|&$`\\\"']", value):
            raise MVMError(
                f"Invalid {component_name} '{value}': must not contain spaces or shell metacharacters"
            )
        return value

    @staticmethod
    def validate_ssh_username(user: str) -> str:
        """Validate SSH username against POSIX conventions.

        Args:
            user: SSH username to validate.

        Returns:
            The validated username.

        Raises:
            MVMError: If username is invalid.
        """
        if not _VALID_SSH_USERNAME.match(user):
            raise MVMError(
                f"Invalid SSH username '{user}': must match ^[a-z_][a-z0-9_-]*$"
            )
        return user

    @staticmethod
    def validate_boot_args(
        boot_args: str, root_uuid: str, guest_ip: str
    ) -> list[str]:
        """Validate boot arguments.

        Args:
            boot_args: Kernel boot arguments
            root_uuid: Root filesystem UUID
            guest_ip: Guest IP address

        Returns:
            List of validation error messages (empty if valid)
        """
        errors: list[str] = []

        if not root_uuid:
            errors.append("root UUID is required")

        if not guest_ip:
            errors.append("guest IP is required")

        if boot_args:
            # Check each boot arg component (split by space)
            for arg in boot_args.split():
                if "=" in arg:
                    key, value = arg.split("=", 1)
                    # Validate the value part
                    try:
                        VMValidator.validate_boot_arg_component(value, key)
                    except MVMError as e:
                        errors.append(str(e))
                else:
                    # No value part, just validate the arg itself
                    try:
                        VMValidator.validate_boot_arg_component(arg, "boot arg")
                    except MVMError as e:
                        errors.append(str(e))

            # Also check root_uuid format if present
            if "root_uuid" in boot_args and root_uuid:
                uuid_pattern = re.compile(
                    r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
                )
                if not uuid_pattern.match(root_uuid):
                    errors.append(f"Invalid root UUID format: {root_uuid}")

        return errors
