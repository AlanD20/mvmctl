"""Data models for Firecracker Manager."""

from fcm.models.vm import VMConfig, VMState, VMInstance
from fcm.models.image import ImageSpec

__all__ = ["VMConfig", "VMState", "VMInstance", "ImageSpec"]
