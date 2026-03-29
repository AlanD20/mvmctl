"""Cloud-init data models."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum, auto
from pathlib import Path
from typing import Any


class CloudInitMode(StrEnum):
    """Cloud-init configuration mode.

    Attributes:
        AUTO: Use default mode (currently nocloud-net).
        ISO: Generate cloud-init ISO from config files.
        CUSTOM: Use a pre-existing custom cloud-init ISO.
        DIRECT_INJECTION: Inject cloud-init files directly into rootfs using libguestfs (filesystem-agnostic).
        DISABLED: Skip cloud-init entirely (no ISO mounted).
        NO_CLOUD_NET: Serve cloud-init files via HTTP (nocloud-net datasource).
    """

    AUTO = "auto"
    ISO = "iso"
    CUSTOM = "custom"
    DIRECT_INJECTION = "direct"  # Inject cloud-init files directly into rootfs using libguestfs (filesystem-agnostic)
    DISABLED = "disabled"
    NO_CLOUD_NET = "nocloud-net"


class CloudInitStatus(StrEnum):
    """Cloud-init execution status based on console log detection."""

    PENDING = auto()
    RUNNING = auto()
    DONE = auto()
    ERROR = auto()


@dataclass
class CloudInitConfig:
    """Cloud-init configuration parameters.

    Attributes:
        mode: Cloud-init configuration mode (auto/custom/disabled/nocloud-net).
        iso_path: Path to custom cloud-init ISO (used when mode is CUSTOM).
        keep_iso: Retain the generated cloud-init ISO after boot.
        nocloud_net_url: URL for nocloud-net HTTP datasource.
    """

    mode: CloudInitMode = CloudInitMode.AUTO
    iso_path: Path | None = None
    keep_iso: bool = False
    nocloud_net_url: str | None = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize CloudInitConfig to a dictionary."""
        return {
            "mode": self.mode.value,
            "iso_path": str(self.iso_path) if self.iso_path else None,
            "keep_iso": self.keep_iso,
            "nocloud_net_url": self.nocloud_net_url,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> CloudInitConfig:
        """Deserialize CloudInitConfig from a dictionary."""
        mode_value = data.get("mode", "auto")
        mode = CloudInitMode(mode_value) if mode_value else CloudInitMode.AUTO

        iso_path_str = data.get("iso_path")
        iso_path = Path(iso_path_str) if iso_path_str else None

        return cls(
            mode=mode,
            iso_path=iso_path,
            keep_iso=data.get("keep_iso", False),
            nocloud_net_url=data.get("nocloud_net_url"),
        )
