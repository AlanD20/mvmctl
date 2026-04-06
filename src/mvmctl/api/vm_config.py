from __future__ import annotations

from pathlib import Path
from typing import Any

from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.models.vm_config_file import VMExportConfig

__all__ = [
    "load_vm_config_file",
    "save_vm_config_file",
    "build_vm_config_file",
    "merge_cli_overrides",
]


def load_vm_config_file(path: Path) -> VMExportConfig:
    return VMExportConfig.from_json_file(path)


def save_vm_config_file(config: VMExportConfig, path: Path) -> None:
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
    ipv4_gateway: str | None = None,
    subnet_mask: str | None = None,
    tap_device: str | None = None,
    cloud_init: dict[str, Any] | None = None,
) -> VMExportConfig:
    from mvmctl.models.vm import VMConfig
    from mvmctl.models.vm_config_file import (
        VMExportBinaryConfig,
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportFirecrackerConfig,
        VMExportImageConfig,
        VMExportKernelConfig,
        VMExportNetworkConfig,
        VMExportBootConfig,
    )

    effective_vcpus = vcpus if vcpus is not None else DEFAULT_VM_VCPU_COUNT
    effective_mem = mem if mem is not None else DEFAULT_VM_MEM_MIB
    effective_network = network if network is not None else DEFAULT_NETWORK_NAME
    effective_user = user if user is not None else DEFAULT_VM_SSH_USER
    effective_api_socket = (
        enable_api_socket if enable_api_socket is not None else DEFAULT_VM_ENABLE_API_SOCKET
    )
    effective_pci = enable_pci if enable_pci is not None else DEFAULT_VM_ENABLE_PCI
    effective_bin = firecracker_bin if firecracker_bin is not None else DEFAULT_FIRECRACKER_BIN_NAME

    from mvmctl.models.vm import VMInstance

    vm_config_kwargs: dict[str, Any] = {
        "name": name,
        "vcpu_count": effective_vcpus,
        "mem_size_mib": effective_mem,
        "enable_api_socket": effective_api_socket,
        "enable_pci": effective_pci,
    }
    if kernel:
        vm_config_kwargs["kernel_path"] = Path(kernel)
    if rootfs_path is not None:
        vm_config_kwargs["rootfs_path"] = rootfs_path

    vm_instance = VMInstance(
        name=name,
        ipv4=ip,
        mac=mac,
        tap_device=tap_device,
        ipv4_gateway=ipv4_gateway,
        subnet_mask=subnet_mask,
    )

    # Build Firecracker boot config (stored as dict for now, to be refactored in Phase 10)
    try:
        generator = ConfigGenerator(VMConfig(**vm_config_kwargs), vm_instance)
        firecracker_boot_config: dict[str, Any] = dict(generator.generate())
    except Exception:
        firecracker_boot_config = {}

    # Parse kernel version if provided (for portable export)
    kernel_version = None
    if kernel:
        # Extract version from path like /path/to/vmlinux-6.1.0
        kernel_path = Path(kernel)
        kernel_version = kernel_path.name  # Default to full filename
        if "-" in kernel_path.name:
            kernel_version = kernel_path.name.split("-")[-1]  # Extract version suffix

    return VMExportConfig(
        name=name,
        compute=VMExportComputeConfig(vcpus=effective_vcpus, mem=effective_mem),
        image=VMExportImageConfig(os_slug=image, arch="x86_64"),  # arch default for now
        kernel=VMExportKernelConfig(version=kernel_version),
        binary=VMExportBinaryConfig(name=effective_bin),
        network=VMExportNetworkConfig(name=effective_network),
        boot=VMExportBootConfig(enable_console=True),  # default for now
        firecracker=VMExportFirecrackerConfig(
            enable_api_socket=effective_api_socket,
            enable_pci=effective_pci,
        ),
        cloud_init=VMExportCloudInitConfig(
            mode="inject" if cloud_init else None,
            user=effective_user,
            ssh_key=ssh_key,
        ),
    )


def merge_cli_overrides(
    base: VMExportConfig,
    *,
    name: str | None = None,
    vcpus: int | None = None,
    mem: int | None = None,
    ip: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user: str | None = None,
    enable_api_socket: bool | None = None,
    enable_pci: bool | None = None,
) -> VMExportConfig:
    """Merge CLI overrides into base config, returning new VMExportConfig."""
    from dataclasses import replace
    from mvmctl.models.vm_config_file import (
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportFirecrackerConfig,
        VMExportNetworkConfig,
    )

    # Build new sub-configs with overrides applied
    new_compute = replace(
        base.compute,
        vcpus=vcpus if vcpus is not None else base.compute.vcpus,
        mem=mem if mem is not None else base.compute.mem,
    )

    new_network = replace(
        base.network,
        ip=ip if ip is not None else base.network.ip,
        mac=mac if mac is not None else base.network.mac,
    )

    new_firecracker = replace(
        base.firecracker,
        enable_api_socket=enable_api_socket
        if enable_api_socket is not None
        else base.firecracker.enable_api_socket,
        enable_pci=enable_pci if enable_pci is not None else base.firecracker.enable_pci,
    )

    new_cloud_init = replace(
        base.cloud_init,
        user=user if user is not None else base.cloud_init.user,
        ssh_key=ssh_key if ssh_key is not None else base.cloud_init.ssh_key,
    )

    return replace(
        base,
        name=name if name is not None else base.name,
        compute=new_compute,
        network=new_network,
        firecracker=new_firecracker,
        cloud_init=new_cloud_init,
    )
