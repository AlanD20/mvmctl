"""Firecracker-related models."""

from typing import TypedDict


class InstanceInfo(TypedDict):
    """Instance info returned from Firecracker get_instance_info()."""

    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    boot_time: str | None


class InstanceDescription(TypedDict):
    """Instance description returned from Firecracker describe_instance()."""

    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    flags: list[str]
    if_addr: dict[str, str]
    used_block_devices: list[str]
