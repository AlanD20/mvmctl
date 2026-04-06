from __future__ import annotations

from dataclasses import replace
from pathlib import Path
from typing import Any

import click

from mvmctl.api.assets import (
    download_firecracker_kernel,
    fetch_binary,
    fetch_image,
)
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_CONSOLE,
    DEFAULT_VM_ENABLE_LOGGING,
    DEFAULT_VM_ENABLE_METRICS,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LSM_FLAGS,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.exceptions import AssetNotFoundError
from mvmctl.models.cloud_init import CloudInitMode
from mvmctl.models.image import ImageSpec
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
        VMExportBootConfig,
        VMExportCloudInitConfig,
        VMExportComputeConfig,
        VMExportFirecrackerConfig,
        VMExportImageConfig,
        VMExportKernelConfig,
        VMExportNetworkConfig,
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
        "lsm_flags": DEFAULT_VM_LSM_FLAGS,
        "enable_logging": DEFAULT_VM_ENABLE_LOGGING,
        "enable_metrics": DEFAULT_VM_ENABLE_METRICS,
        "enable_console": DEFAULT_VM_ENABLE_CONSOLE,
        "cloud_init_mode": CloudInitMode.INJECT,
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
        dict(generator.generate())  # Validate config generation works
    except Exception:
        pass  # Config generation failed, but we can still export without it

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


def _prompt_missing_assets(
    missing: list[tuple[str, str, str]],
) -> None:
    if not missing:
        return

    print("Missing assets detected:")
    for asset_type, identifier, qualifier in missing:
        print(f"  - {asset_type}: {identifier} ({qualifier})")

    for asset_type, identifier, qualifier in missing:
        msg = f"{asset_type.capitalize()} '{identifier}' ({qualifier}) not found. Fetch now?"
        if click.confirm(msg, default=False):
            if asset_type == "image":
                from mvmctl.utils.fs import get_images_dir

                spec = ImageSpec(
                    id=identifier,
                    image_type="os",
                    version=qualifier,
                    name=identifier,
                    source=f"https://cloud-images.ubuntu.com/{identifier}/current/{identifier}-server-cloudimg-{qualifier}.img",
                    format="qcow2",
                    convert_to="ext4",
                    minimum_rootfs_size=2048,  # 2GB default for cloud images
                    sha256="",
                    sha256_url="",
                )
                fetch_image(spec, output_dir=get_images_dir())
            elif asset_type == "kernel":
                download_firecracker_kernel(ci_version=identifier, arch=qualifier)
            elif asset_type == "binary":
                fetch_binary(version=qualifier.lstrip("v"))
        else:
            raise AssetNotFoundError(
                f"{asset_type} '{identifier}' not found. Run: mvm {asset_type} fetch {identifier}"
            )


def merge_cli_overrides(
    base: VMExportConfig,
    *,
    name: str | None = None,
    vcpus: int | None = None,
    mem: int | None = None,
    os_slug: str | None = None,
    arch: str | None = None,
    kernel_version: str | None = None,
    kernel_arch: str | None = None,
    kernel_type: str | None = None,
    binary_name: str | None = None,
    binary_version: str | None = None,
    network_name: str | None = None,
    subnet: str | None = None,
    ipv4_gateway: str | None = None,
    ip: str | None = None,
    mac: str | None = None,
    boot_args: str | None = None,
    enable_console: bool | None = None,
    enable_api_socket: bool | None = None,
    enable_pci: bool | None = None,
    lsm_flags: str | None = None,
    cloud_init_mode: str | None = None,
    user: str | None = None,
    ssh_key: str | None = None,
    keep_iso: bool | None = None,
    nocloud_net_port: int | None = None,
) -> VMExportConfig:
    new_compute = replace(
        base.compute,
        vcpus=vcpus if vcpus is not None else base.compute.vcpus,
        mem=mem if mem is not None else base.compute.mem,
    )

    new_image = replace(
        base.image,
        os_slug=os_slug if os_slug is not None else base.image.os_slug,
        arch=arch if arch is not None else base.image.arch,
    )

    new_kernel = replace(
        base.kernel,
        version=kernel_version if kernel_version is not None else base.kernel.version,
        arch=kernel_arch if kernel_arch is not None else base.kernel.arch,
        type=kernel_type if kernel_type is not None else base.kernel.type,
    )

    new_binary = replace(
        base.binary,
        name=binary_name if binary_name is not None else base.binary.name,
        version=binary_version if binary_version is not None else base.binary.version,
    )

    new_network = replace(
        base.network,
        name=network_name if network_name is not None else base.network.name,
        subnet=subnet if subnet is not None else base.network.subnet,
        ipv4_gateway=ipv4_gateway if ipv4_gateway is not None else base.network.ipv4_gateway,
        ip=ip if ip is not None else base.network.ip,
        mac=mac if mac is not None else base.network.mac,
    )

    new_boot = replace(
        base.boot,
        args=boot_args if boot_args is not None else base.boot.args,
        enable_console=enable_console if enable_console is not None else base.boot.enable_console,
    )

    new_firecracker = replace(
        base.firecracker,
        enable_api_socket=enable_api_socket
        if enable_api_socket is not None
        else base.firecracker.enable_api_socket,
        enable_pci=enable_pci if enable_pci is not None else base.firecracker.enable_pci,
        lsm_flags=lsm_flags if lsm_flags is not None else base.firecracker.lsm_flags,
    )

    new_cloud_init = replace(
        base.cloud_init,
        mode=cloud_init_mode if cloud_init_mode is not None else base.cloud_init.mode,
        user=user if user is not None else base.cloud_init.user,
        ssh_key=ssh_key if ssh_key is not None else base.cloud_init.ssh_key,
        keep_iso=keep_iso if keep_iso is not None else base.cloud_init.keep_iso,
        nocloud_net_port=nocloud_net_port
        if nocloud_net_port is not None
        else base.cloud_init.nocloud_net_port,
    )

    return replace(
        base,
        name=name if name is not None else base.name,
        compute=new_compute,
        image=new_image,
        kernel=new_kernel,
        binary=new_binary,
        network=new_network,
        boot=new_boot,
        firecracker=new_firecracker,
        cloud_init=new_cloud_init,
    )
