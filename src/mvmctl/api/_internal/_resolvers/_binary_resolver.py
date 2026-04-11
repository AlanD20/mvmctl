"""Binary resolution helpers."""

from __future__ import annotations

__all__ = [
    "BinaryResolver",
]


class BinaryResolver:
    """Resolver for firecracker binary."""

    def __init__(self) -> None:
        from mvmctl.core.mvm_db import MVMDatabase

        self._db = MVMDatabase()

    def resolve(self, binary_id: str | None) -> str:
        """Resolve binary ID to binary path.

        Args:
            binary_id: Binary ID or None for default

        Returns:
            Path to firecracker binary

        Raises:
            VMCreateError: If no binary found
        """
        if binary_id is None:
            default_binary = self._db.get_default_binary("firecracker")
            if default_binary is None:
                from mvmctl.exceptions import VMCreateError

                raise VMCreateError(
                    "No firecracker binary specified and no default set. Run 'mvm bin fetch' first."
                )
            return default_binary.path

        binary = self._db.get_binary(binary_id)
        if binary is None:
            from mvmctl.exceptions import VMCreateError

            raise VMCreateError(f"Binary not found: {binary_id}")
        return binary.path
