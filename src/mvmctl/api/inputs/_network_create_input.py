"""Network creation resolver - resolves and validates network creation inputs."""

from __future__ import annotations

import ipaddress
from dataclasses import dataclass, field

from mvmctl.core._internal._db import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import NetworkItem
from mvmctl.utils.network import bridge_name_for, ipv4_gateway_for_subnet
from mvmctl.utils.validation import (
    validate_bridge_name,
    validate_entity_name,
    validate_ipv4_address,
    validate_subnet,
)

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

        # Validate name
        name = validate_entity_name(self._inputs.name, "network")

        # Validate subnet
        subnet = validate_subnet(self._inputs.subnet)

        # Resolve or compute gateway
        if self._inputs.ipv4_gateway is not None:
            ipv4_gateway = validate_ipv4_address(
                self._inputs.ipv4_gateway,
                require_private=True,
                subnet=self._inputs.subnet,
            )
        else:
            ipv4_gateway = ipv4_gateway_for_subnet(subnet)

        # Compute bridge name
        bridge = bridge_name_for(name)
        bridge = validate_bridge_name(bridge)

        # Compute network ID
        from datetime import datetime, timezone

        from mvmctl.utils.full_hash import generate_full_hash_network

        created_at = datetime.now(tz=timezone.utc).isoformat()
        network_id = generate_full_hash_network(name, subnet, created_at)

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

        # Check if network already exists
        existing = self._network_repo.get_by_name(self._result.name)
        if existing is not None:
            raise NetworkError(f"Network '{self._result.name}' already exists")

        # Validate no subnet overlap
        existing_networks = self._network_repo.list_all()
        self._validate_subnet_no_overlap(
            self._result.subnet, existing_networks, self._result.name
        )

        # Validate no bridge conflict
        self._validate_bridge_not_conflicting(
            self._result.bridge, existing_networks, self._result.name
        )

    @staticmethod
    def _validate_subnet_no_overlap(
        subnet: str, existing: list[NetworkItem], exclude_name: str = ""
    ) -> None:
        """Check that subnet doesn't overlap with existing networks."""
        new_net = ipaddress.IPv4Network(subnet, strict=False)
        for item in existing:
            if item.name == exclude_name:
                continue
            existing_net = ipaddress.IPv4Network(item.subnet, strict=False)
            if new_net.overlaps(existing_net):
                raise NetworkError(
                    f"Subnet {subnet} overlaps with network '{item.name}' ({item.subnet})"
                )

    @staticmethod
    def _validate_bridge_not_conflicting(
        bridge: str, existing: list[NetworkItem], exclude_name: str = ""
    ) -> None:
        """Check that bridge name doesn't conflict with existing networks."""
        for item in existing:
            if item.name == exclude_name:
                continue
            if item.bridge == bridge:
                raise NetworkError(
                    f"Bridge name '{bridge}' conflicts with network '{item.name}'"
                )
