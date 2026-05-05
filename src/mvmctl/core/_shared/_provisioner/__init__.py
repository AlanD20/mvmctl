"""Provisioner abstraction — unified interface for guestfs and loop-mount backends."""

from __future__ import annotations

from mvmctl.core._shared._provisioner._provisioner import Provisioner

__all__ = ["Provisioner"]
