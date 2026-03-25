from __future__ import annotations

from pathlib import Path
from typing import Any

from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
)
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.models.vm_config_file import VMCreateConfigFile


def _vm_defaults() -> Any:
    from mvmctl.core.config import load_config
    from mvmctl.utils.fs import get_assets_dir

    return load_config(get_assets_dir()).vm_defaults


def load_vm_config_file(path: Path) -> VMCreateConfigFile:
    return VMCreateConfigFile.from_json_file(path)


def save_vm_config_file(config: VMCreateConfigFile, path: Path) -> None:
    config.to_json_file(path)


def build_vm_config_file(
    name: str,
    image: str,
    kernel: str | None = None,
    vcpus: int | None = None,
    mem: int | None = None,
    ip: str | None = None,
    network: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user: str | None = None,
    enable_api_socket: bool | None = None,
    enable_pci: bool | None = None,
    firecracker_bin: str | None = None,
    rootfs_path: Path | None = None,
    gateway: str | None = None,
    subnet_mask: str | None = None,
    tap_device: str | None = None,
) -> VMCreateConfigFile:
    from mvmctl.models.vm import VMConfig

    vm_defaults = _vm_defaults()
    effective_vcpus = vcpus if vcpus is not None else vm_defaults.vcpu_count
    effective_mem = mem if mem is not None else vm_defaults.mem_size_mib
    effective_network = network if network is not None else DEFAULT_NETWORK_NAME
    effective_user = user if user is not None else vm_defaults.ssh_user
    effective_api_socket = (
        enable_api_socket if enable_api_socket is not None else vm_defaults.enable_api_socket
    )
    effective_pci = enable_pci if enable_pci is not None else vm_defaults.enable_pci
    effective_bin = firecracker_bin if firecracker_bin is not None else DEFAULT_FIRECRACKER_BIN_NAME

    vm_config_kwargs: dict[str, Any] = {
        "name": name,
        "vcpu_count": effective_vcpus,
        "mem_size_mib": effective_mem,
        "guest_ip": ip,
        "guest_mac": mac,
        "tap_device": tap_device,
        "enable_api_socket": effective_api_socket,
        "enable_pci": effective_pci,
    }
    if kernel:
        vm_config_kwargs["kernel_path"] = Path(kernel)
    if rootfs_path is not None:
        vm_config_kwargs["rootfs_path"] = rootfs_path
    if gateway is not None:
        vm_config_kwargs["gateway"] = gateway
    if subnet_mask is not None:
        vm_config_kwargs["subnet_mask"] = subnet_mask

    try:
        generator = ConfigGenerator(VMConfig(**vm_config_kwargs))
        firecracker_config: dict[str, Any] = dict(generator.generate())
    except Exception:
        firecracker_config = {}

    return VMCreateConfigFile(
        name=name,
        image=image,
        kernel=kernel,
        vcpus=effective_vcpus,
        mem=effective_mem,
        ip=ip,
        network=effective_network,
        mac=mac,
        ssh_key=ssh_key,
        user=effective_user,
        enable_api_socket=effective_api_socket,
        enable_pci=effective_pci,
        firecracker_bin=effective_bin,
        firecracker_config=firecracker_config,
    )


def merge_cli_overrides(
    base: VMCreateConfigFile,
    *,
    name: str | None = None,
    image: str | None = None,
    kernel: str | None = None,
    vcpus: int | None = None,
    mem: int | None = None,
    ip: str | None = None,
    network: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user: str | None = None,
    enable_api_socket: bool | None = None,
    enable_pci: bool | None = None,
    firecracker_bin: str | None = None,
) -> VMCreateConfigFile:
    return VMCreateConfigFile(
        name=name if name is not None else base.name,
        image=image if image is not None else base.image,
        kernel=kernel if kernel is not None else base.kernel,
        vcpus=vcpus if vcpus is not None else base.vcpus,
        mem=mem if mem is not None else base.mem,
        ip=ip if ip is not None else base.ip,
        network=network if network is not None else base.network,
        mac=mac if mac is not None else base.mac,
        ssh_key=ssh_key if ssh_key is not None else base.ssh_key,
        user=user if user is not None else base.user,
        enable_api_socket=(
            enable_api_socket if enable_api_socket is not None else base.enable_api_socket
        ),
        enable_pci=enable_pci if enable_pci is not None else base.enable_pci,
        firecracker_bin=(firecracker_bin if firecracker_bin is not None else base.firecracker_bin),
        firecracker_config=base.firecracker_config,
    )
