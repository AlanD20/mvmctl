"""Network input models for API boundary — existing resource actions."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from mvmctl.core._shared import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver
from mvmctl.exceptions import NetworkNotFoundError
from mvmctl.models import NetworkItem

if TYPE_CHECKING:
    pass

__all__ = ["NetworkInput", "NetworkRequest", "ResolvedNetworkInput"]


@dataclass
class NetworkInput:
    """
    Input model for identifying existing networks.

    Used for operations on existing networks (remove, get, inspect, default).
    Provides identifiers (name or id) to resolve the network from DB.
    """

    name: list[str] = field(default_factory=list)
    id: list[str] = field(default_factory=list)

    force: bool | None = None


@dataclass(frozen=True)
class ResolvedNetworkInput:
    """
    Immutable resolved network request — contains resolved NetworkItem records.

    These records are guaranteed to exist in the DB, making them safe to operate on.
    """

    networks: list[NetworkItem]

    force: bool | None = None


class NetworkRequest:
    """
    Resolve network identifiers to DB records and validate.

    Takes NetworkInput (names/ids) and resolves them to NetworkItem records
    using NetworkResolver. Calls ensure_validate() after resolution.
    """

    _result: ResolvedNetworkInput | None = None

    def __init__(
        self, *, inputs: NetworkInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._network_resolver = NetworkResolver(
            NetworkRepository(self._db),
            include=["leases"],
        )

    @property
    def result(self) -> ResolvedNetworkInput | None:
        return self._result

    def resolve(self) -> ResolvedNetworkInput:
        """
        Resolve network identifiers to NetworkItem records.

        Returns:
            ResolvedNetworkInput with resolved network records.

        Raises:
            NetworkNotFoundError: If any identifier cannot be resolved.

        """
        identifiers = self._inputs.name + self._inputs.id

        if not identifiers:
            raise NetworkNotFoundError("No network identifiers provided")

        result = self._network_resolver.resolve_many(identifiers)

        if result.errors and not result.items:
            raise NetworkNotFoundError(
                f"Could not resolve any networks: {', '.join(result.errors)}"
            )

        self._result = ResolvedNetworkInput(
            networks=result.items,
            force=self._inputs.force if self._inputs.force else False,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved network inputs."""
        if self._result is None:
            raise NetworkNotFoundError(
                "Failed to resolve necessary dependencies to validate"
            )

        if not self._result.networks:
            raise NetworkNotFoundError("No networks found matching identifiers")
