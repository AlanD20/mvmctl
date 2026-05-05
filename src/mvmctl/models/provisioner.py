"""Provisioner type definitions."""

from __future__ import annotations

from enum import StrEnum


class ProvisionerType(StrEnum):
    """Which provisioning mechanism to use for a VM's root filesystem."""

    LOOP_MOUNT = "loop_mount"
    GUESTFS = "guestfs"
