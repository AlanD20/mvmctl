"""Network resolution helpers."""

from __future__ import annotations

__all__ = [
    "NetworkResolver",
]


class NetworkResolver:
    """Resolver for network configuration."""

    def __init__(self) -> None:
        from mvmctl.core.mvm_db import MVMDatabase

        self._db = MVMDatabase()

    def resolve(self, network_name: str | None) -> tuple[str, str]:
        """Resolve network name to name and network_id.

        Args:
            network_name: Network name or None for default

        Returns:
            Tuple of (network_name, network_id)
        """
        if network_name is None:
            default_network = self._db.get_default_network()
            if default_network is None:
                from mvmctl.exceptions import NetworkError

                raise NetworkError("No default network configured. Run 'mvm network create' first.")
            return default_network.name, default_network.id

        network = self._db.get_network_by_name(network_name)
        if network is None:
            from mvmctl.exceptions import NetworkError

            raise NetworkError(f"Network not found: {network_name}")
        return network.name, network.id
