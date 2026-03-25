from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from fcm.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)


@dataclass
class VMCreateConfigFile:
    name: str
    image: str
    kernel: str | None = None
    vcpus: int = DEFAULT_VM_VCPU_COUNT
    mem: int = DEFAULT_VM_MEM_MIB
    ip: str | None = None
    network: str = DEFAULT_NETWORK_NAME
    mac: str | None = None
    ssh_key: str | None = None
    user: str = DEFAULT_VM_SSH_USER
    enable_api_socket: bool = False
    enable_pci: bool = False
    firecracker_bin: str = DEFAULT_FIRECRACKER_BIN_NAME
    firecracker_config: dict[str, Any] | None = None

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> VMCreateConfigFile:
        known = {
            "name",
            "image",
            "kernel",
            "vcpus",
            "mem",
            "ip",
            "network",
            "mac",
            "ssh_key",
            "user",
            "enable_api_socket",
            "enable_pci",
            "firecracker_bin",
            "firecracker_config",
        }
        filtered = {k: v for k, v in data.items() if k in known}
        return cls(**filtered)

    @classmethod
    def from_json_file(cls, path: Path) -> VMCreateConfigFile:
        if not path.exists():
            raise FileNotFoundError(f"VM config file not found: {path}")
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError as e:
            raise ValueError(f"Invalid JSON in VM config file {path}: {e}") from e
        if not isinstance(data, dict):
            raise ValueError(f"VM config file must be a JSON object: {path}")
        return cls.from_dict(data)

    def to_json_file(self, path: Path) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.to_dict(), indent=2, default=str))
