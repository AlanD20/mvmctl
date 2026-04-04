from __future__ import annotations

import ipaddress
import secrets

from mvmctl.constants import DEFAULT_GUEST_MAC_PREFIX


def subnet_mask_from_subnet(subnet: str) -> str:
    return str(ipaddress.IPv4Network(subnet, strict=False).netmask)


def prefix_len_from_subnet(subnet: str) -> int:
    return ipaddress.IPv4Network(subnet, strict=False).prefixlen


def ipv4_gateway_for_subnet(subnet: str) -> str:
    net = ipaddress.IPv4Network(subnet, strict=False)
    return str(next(iter(net.hosts())))


def bridge_name_for(network_name: str) -> str:
    from mvmctl.constants import device_prefix

    truncated = network_name[:10]
    return f"{device_prefix()}-{truncated}"


def generate_mac() -> str:
    rand_bytes = secrets.token_bytes(4)
    suffix = ":".join(f"{b:02x}" for b in rand_bytes)
    return f"{DEFAULT_GUEST_MAC_PREFIX}:{suffix}"
