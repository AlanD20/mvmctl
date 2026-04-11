"""Network resolution helpers."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.db.models import Network
from mvmctl.exceptions import NetworkNotFoundError

__all__ = [
    "NetworkResolver",
    "NetworkResolveResult",
]


@dataclass
class NetworkResolveResult:
    items: list[Network]
    errors: list[str]
    exit_code: int


class NetworkResolver:
    """Resolver for network configuration."""

    def __init__(self) -> None:
        from mvmctl.core.mvm_db import MVMDatabase

        self._db = MVMDatabase()

    def by_id(self, network_id: str) -> Network:
        """Resolve network by ID prefix."""
        matches = self._db.find_networks_by_prefix(network_id)
        if len(matches) == 0:
            raise NetworkNotFoundError(f"Network not found: {network_id}")
        if len(matches) > 1:
            raise NetworkNotFoundError(f"Network ID is ambiguous: {network_id}")
        return matches[0]

    def by_name(self, name: str) -> Network:
        """Resolve network by name."""
        network = self._db.get_network_by_name(name)
        if network is None:
            raise NetworkNotFoundError(f"Network not found: {name}")
        return network

    def resolve(self, value: str) -> Network:
        """Resolve network by name or ID prefix."""
        try:
            return self.by_name(value)
        except NetworkNotFoundError:
            pass
        return self.by_id(value)

    def resolve_many(self, identifiers: list[str]) -> NetworkResolveResult:
        """Resolve multiple network identifiers by name or id."""
        items: list[Network] = []
        errors: list[str] = []

        for identifier in identifiers:
            try:
                item = self.resolve(identifier)
                if item not in items:
                    items.append(item)
            except Exception as e:
                errors.append(f"{identifier}: {e}")

        exit_code = 1 if errors and not items else 0
        return NetworkResolveResult(items=items, errors=errors, exit_code=exit_code)
