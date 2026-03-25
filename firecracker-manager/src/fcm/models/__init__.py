"""Data models for Firecracker Manager."""

from fcm.models.image import ImageSpec
from fcm.models.vm import VMConfig, VMInstance, VMState

__all__ = ["VMConfig", "VMState", "VMInstance", "ImageSpec"]
