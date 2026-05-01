from enum import StrEnum, auto


class CloudInitMode(StrEnum):
    """
    Cloud-init configuration mode.

    Attributes:
        INJECT: Inject cloud-init files directly into rootfs using libguestfs (filesystem-agnostic).
        NET: Serve cloud-init files via HTTP (nocloud-net datasource).
        OFF: Skip cloud-init entirely (no ISO mounted).
        ISO: Generate cloud-init ISO from config files.

    """

    INJECT = "inject"
    NET = "net"
    OFF = "off"
    ISO = "iso"


class CloudInitStatus(StrEnum):
    """Cloud-init execution status based on console log detection."""

    PENDING = auto()
    RUNNING = auto()
    DONE = auto()
    ERROR = auto()


__all__ = ["CloudInitMode", "CloudInitStatus"]
