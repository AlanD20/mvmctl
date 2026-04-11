"""Network generation helpers for API modules."""

from __future__ import annotations

import random
from typing import Final

from mvmctl.constants import CONST_BYTE_MAX

# MAC address prefix for mvm VMs
MAC_PREFIX: Final[str] = "52:54:00:7f"

__all__ = [
    "generate_mac_address",
    "generate_tap_device_name",
]


def generate_mac_address() -> str:
    """Generate a random MAC address with the mvm prefix.

    Returns:
        MAC address string in format XX:XX:XX:XX:XX:XX
    """
    suffix = ":".join(f"{random.randint(0, CONST_BYTE_MAX):02x}" for _ in range(3))
    return f"{MAC_PREFIX}:{suffix}"


def generate_tap_device_name(net_name: str, vm_name: str) -> str:
    """Generate a TAP device name based on network and VM names.

    Args:
        net_name: Network name (first 3 chars used)
        vm_name: VM name (first 3 chars used)

    Returns:
        TAP device name in format tap-NNN-VVV-RND
        where NNN = net_name[:3], VVV = vm_name[:3], RND = random 2 hex digits
    """
    net_prefix = net_name[:3].lower()
    vm_prefix = vm_name[:3].lower()
    rnd = f"{random.randint(0, CONST_BYTE_MAX):02x}"
    return f"tap-{net_prefix}-{vm_prefix}-{rnd}"
