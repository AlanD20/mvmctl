"""Image validation utilities."""

from __future__ import annotations

import re

from mvmctl.utils.common import CommonUtils


class ImageValidator:
    """Validate image-specific inputs."""

    @staticmethod
    def validate_name(name: str) -> str:
        """Validate image name.

        Args:
            name: Image name to validate.

        Returns:
            The validated name.

        Raises:
            MVMError: If name is invalid.
        """
        return CommonUtils.validate_entity_name(name, entity_type="image")

    @staticmethod
    def validate_fs_uuid(uuid: str | None, field_name: str = "fs_uuid") -> None:
        """Validate filesystem UUID format.

        Args:
            uuid: UUID string to validate
            field_name: Field name for error messages

        Raises:
            MVMError: If UUID format is invalid
        """
        if uuid is None:
            return
        uuid_pattern = re.compile(
            r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"
        )
        if not uuid_pattern.match(uuid):
            from mvmctl.exceptions import MVMError

            raise MVMError(
                f"Invalid {field_name} format: '{uuid}'. "
                "Expected format: XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX"
            )

    @staticmethod
    def validate_fs_type(
        fs_type: str | None, field_name: str = "fs_type"
    ) -> None:
        """Validate filesystem type.

        Args:
            fs_type: Filesystem type string
            field_name: Field name for error messages

        Raises:
            MVMError: If filesystem type is invalid
        """
        if fs_type is None:
            return
        supported_types = {"ext4", "btrfs", "xfs", "ext3", "ext2"}
        if fs_type.lower() not in supported_types:
            from mvmctl.exceptions import MVMError

            raise MVMError(
                f"Invalid {field_name}: '{fs_type}'. "
                f"Supported types: {', '.join(sorted(supported_types))}"
            )
