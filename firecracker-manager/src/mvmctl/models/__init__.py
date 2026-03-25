"""Data models for Firecracker Manager."""

from mvmctl.models.image import ImageSpec
from mvmctl.models.vm import VMConfig, VMInstance, VMState

__all__ = ["VMConfig", "VMState", "VMInstance", "ImageSpec"]
