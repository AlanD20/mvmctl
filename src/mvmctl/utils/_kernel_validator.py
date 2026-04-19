"""Kernel validation utilities."""

from __future__ import annotations

from mvmctl.utils.common import CommonUtils


class KernelValidator:
    """Validate kernel-specific inputs."""

    @staticmethod
    def validate_name(name: str) -> str:
        """Validate kernel name.

        Args:
            name: Kernel name to validate.

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.
        """
        return CommonUtils.validate_entity_name(name, entity_type="kernel")
