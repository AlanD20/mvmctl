"""Network domain - Network configuration and IP lease management."""

from __future__ import annotations

from mvmctl.core.network._iptables_resolver import IPTablesRuleResolver
from mvmctl.core.network._lease_resolver import NetworkLeaseResolver
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._resolver import NetworkResolver, NetworkResolveResult
from mvmctl.core.network._service import NetworkService

__all__ = [
    "NetworkRepository",
    "LeaseRepository",
    "NetworkResolver",
    "NetworkResolveResult",
    "NetworkService",
    "LeaseService",
    "NetworkLeaseResolver",
    "IPTablesRuleResolver",
]
