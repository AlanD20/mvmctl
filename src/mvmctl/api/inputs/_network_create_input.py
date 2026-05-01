"""Network creation resolver - resolves and validates network creation inputs."""

from __future__ import annotations

from dataclasses import dataclass, field

from mvmctl.core._shared import Database
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkError
from mvmctl.utils._validators import NetworkValidator
from mvmctl.utils.network import NetworkUtils

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
        """Resolve all inputs to explicit values.

        This method resolves DB-backed defaults and computes derived values
        (gateway, bridge name). It does NOT validate —
        validation happens in ensure_validate().
        """
        # Resolve or compute gateway
        if self._inputs.ipv4_gateway is not None:
            ipv4_gateway = self._inputs.ipv4_gateway
        else:
            ipv4_gateway = NetworkUtils.compute_ipv4_gateway(
                self._inputs.subnet
            )

        # Compute bridge name
        bridge = NetworkUtils.compute_bridge_name(self._inputs.name)

        # Build result
        self._result = ResolvedNetworkCreateRequest(
            name=self._inputs.name,
            subnet=self._inputs.subnet,
            ipv4_gateway=ipv4_gateway,
            bridge=bridge,
            nat_enabled=self._inputs.nat_enabled,
            nat_gateways=self._inputs.nat_gateways,
        )

        # Validate
        self.ensure_validate()

        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved network creation inputs.

        Validates:
        - Network name format
        - Subnet format and overlap with existing networks
        - Gateway format and membership in subnet
        - Bridge name format and conflicts
        - NAT gateways format and interface existence
        - Network name doesn't already exist
        """
        if self._result is None:
            raise NetworkError(
                "Failed to resolve necessary dependencies to validate"
            )

        validator = NetworkValidator()

        # Validate name (no dots, lowercase only)
        validator.validate_name(self._result.name)

        # Validate and normalize subnet
        validator.validate_subnet(self._result.subnet)

        # Validate gateway is in subnet
        validator.validate_ipv4_gateway(
            self._result.ipv4_gateway, subnet=self._result.subnet
        )

        # Validate bridge name
        validator.validate_bridge_name(self._result.bridge)

        # Validate NAT gateways
        if self._result.nat_gateways:
            validator.validate_nat_gateways(self._result.nat_gateways)

        # Check if network already exists
        existing = self._network_repo.get_by_name(self._result.name)
        if existing is not None:
            raise NetworkError(f"Network '{self._result.name}' already exists")

        # Validate no subnet overlap
        existing_networks = self._network_repo.list_all()
        validator.validate_subnet_no_overlap(
            self._result.subnet, existing_networks, self._result.name
        )
