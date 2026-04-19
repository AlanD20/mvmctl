"""Network creation resolver - resolves and validates network creation inputs."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._internal._db import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkError
from mvmctl.utils.full_hash import HashGenerator
from mvmctl.utils.network import NetworkUtils
from mvmctl.utils.network_validator import NetworkValidator

__all__ = [
    "NetworkCreateInput",
    "NetworkCreateRequest",
    "ResolvedNetworkCreateRequest",
]


@dataclass
class NetworkCreateInput:
    """Input model for network creation — raw CLI parameters.

    Optional fields are None when not provided by the user.
    DB-backed defaults are resolved by NetworkCreateRequest.
    """

    name: str
    subnet: str
    ipv4_gateway: str | None = None
    nat_enabled: bool = True
    nat_gateways: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class ResolvedNetworkCreateRequest:
    """Immutable resolved inputs for network creation — all values explicit.

    Output of NetworkCreateRequest.resolve(). No None values for required fields.
    """

    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    network_id: str
    created_at: str


class NetworkCreateRequest:
    """Resolve and validate network creation inputs.

    Takes NetworkCreateInput and resolves DB-backed defaults,
    validates subnet overlap and bridge conflicts, and produces
    a ResolvedNetworkCreateRequest suitable for network creation.
    """

    _result: ResolvedNetworkCreateRequest | None = None

    def __init__(
        self, *, inputs: NetworkCreateInput, db: Database | None = None
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._network_repo = NetworkRepository(self._db)

    @property
    def result(self) -> ResolvedNetworkCreateRequest | None:
        return self._result

    def resolve(self) -> ResolvedNetworkCreateRequest:
        """Resolve all inputs to explicit values and validate."""
        validator = NetworkValidator()

        # Validate name (no dots, lowercase only)
        name = validator.validate_name(self._inputs.name)

        # Validate and normalize subnet
        subnet = validator.validate_subnet(self._inputs.subnet)

        # Resolve or compute gateway — uses NORMALIZED subnet
        if self._inputs.ipv4_gateway is not None:
            ipv4_gateway = validator.validate_ipv4_gateway(
                self._inputs.ipv4_gateway, subnet=subnet
            )
        else:
            ipv4_gateway = NetworkUtils.compute_ipv4_gateway(subnet)

        # Compute and validate bridge name
        bridge = NetworkUtils.compute_bridge_name(name)
        bridge = validator.validate_bridge_name(bridge)

        # Compute network ID
        from datetime import datetime, timezone

        created_at = datetime.now(tz=timezone.utc).isoformat()
        network_id = HashGenerator.network(name, subnet, created_at)

        # Build result
        self._result = ResolvedNetworkCreateRequest(
            name=name,
            subnet=subnet,
            ipv4_gateway=ipv4_gateway,
            bridge=bridge,
            nat_enabled=self._inputs.nat_enabled,
            nat_gateways=self._inputs.nat_gateways,
            network_id=network_id,
            created_at=created_at,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved network creation inputs.

        Validates:
        - Network name doesn't already exist
        - Subnet doesn't overlap with existing networks
        - Bridge name doesn't conflict with existing networks
        """
        if self._result is None:
            raise NetworkError(
                "Failed to resolve necessary dependencies to validate"
            )

        validator = NetworkValidator()

        # Check if network already exists
        existing = self._network_repo.get_by_name(self._result.name)
        if existing is not None:
            raise NetworkError(f"Network '{self._result.name}' already exists")

        # Validate no subnet overlap
        existing_networks = self._network_repo.list_all()
        validator.validate_subnet_no_overlap(
            self._result.subnet, existing_networks, self._result.name
        )

        # Validate no bridge conflict
        validator.validate_bridge_not_conflicting(
            self._result.bridge, existing_networks, self._result.name
        )
