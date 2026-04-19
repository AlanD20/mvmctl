"""Binary validation utilities."""

from __future__ import annotations

from mvmctl.utils.common import CommonUtils


class BinaryValidator:
    """Validate binary-specific inputs."""

    @staticmethod
    def validate_name(name: str) -> str:
        """Validate binary name.

        Args:
            name: Binary name to validate.

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.
        """
        return CommonUtils.validate_entity_name(name, entity_type="binary")
