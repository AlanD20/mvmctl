"""CLI-specific helpers."""

from typing import Optional

import typer

from mvmctl.api.config import (
    FirecrackerConfig,
    MVMConfig,
    NetworkDefaultsConfig,
    PathsConfig,
    VMDefaultsConfig,
)
from mvmctl.api.vms import get_vm_manager
from mvmctl.constants import (
    DEFAULT_FIRECRACKER_BINARY_PATH,
    DEFAULT_NETWORK_IPV4_GATEWAY,
    DEFAULT_NETWORK_NAME,
    DEFAULT_NETWORK_SUBNET,
    DEFAULT_VM_BOOT_ARGS,
    DEFAULT_VM_DISK_SIZE,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_LSM_FLAGS,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_NETWORK_INTERFACE,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.console import print_error
from mvmctl.utils.fs import get_cache_dir
from mvmctl.utils.validation import is_ip_address, validate_entity_name


def check_name_arg(ctx: typer.Context, name: str | None) -> str:
    """Guard for positional name arg: show help on ``"help"`` or ``None``, else return name."""
    if name == "help":
        typer.echo(ctx.get_help())
        raise typer.Exit()
    if name is None:
        typer.echo(ctx.get_help())
        raise typer.Exit(code=1)
    return name


def resolve_vm_by_id_or_name(vm_id: Optional[str], name: Optional[str]) -> str:
    """Resolve a VM by ID prefix or name.

    Args:
        vm_id: VM ID prefix or name (positional argument)
        name: VM name (from --name option)

    Returns:
        Resolved VM name

    Raises:
        typer.Exit: If no VM found or ambiguous match
    """
    manager = get_vm_manager()

    if name:
        if manager.get(name) is None:
            print_error(f"VM '{name}' not found")
            raise typer.Exit(1)
        return name

    if vm_id:
        matches = manager.find_by_id_prefix(vm_id)
        if len(matches) == 1:
            return matches[0].name
        if len(matches) > 1:
            print_error(f"Multiple VMs match ID prefix '{vm_id}' — use a longer prefix or --name")
            raise typer.Exit(1)
        if manager.get(vm_id) is not None:
            return vm_id
        print_error(f"No VM found with ID prefix or name '{vm_id}'")
        raise typer.Exit(1)

    print_error("Provide a VM ID prefix or --name")
    raise typer.Exit(1)


def resolve_ssh_target(
    vm_id: Optional[str],
    name: Optional[str],
    ip: Optional[str],
) -> str:
    """Resolve SSH target from vm_id, name, or IP.

    Args:
        vm_id: VM identifier (name, ID prefix, or IP)
        name: VM name from --name option
        ip: IP address from --ip option

    Returns:
        Resolved target (VM name or IP)

    Raises:
        MVMError: If no valid target provided or ambiguous match
    """
    if ip is not None:
        return ip

    if name is not None:
        validate_entity_name(name, "VM")
        return name

    if vm_id is not None:
        if is_ip_address(vm_id):
            return vm_id
        manager = get_vm_manager()
        matches = manager.find_by_id_prefix(vm_id)
        if len(matches) == 1:
            return matches[0].name
        elif len(matches) > 1:
            raise MVMError(f"Ambiguous ID prefix '{vm_id}' matches {len(matches)} VMs")
        else:
            validate_entity_name(vm_id, "VM")
            return vm_id

    raise MVMError("Provide either a VM identifier, --name, or --ip")


def build_mvm_defaults() -> MVMConfig:
    """Build the default MVMConfig for CLI-layer config loading."""
    return MVMConfig(
        firecracker=FirecrackerConfig(binary=DEFAULT_FIRECRACKER_BINARY_PATH),
        vm_defaults=VMDefaultsConfig(
            vcpu_count=DEFAULT_VM_VCPU_COUNT,
            mem_size_mib=DEFAULT_VM_MEM_MIB,
            ssh_user=DEFAULT_VM_SSH_USER,
            network_interface=DEFAULT_VM_NETWORK_INTERFACE,
            boot_args=DEFAULT_VM_BOOT_ARGS,
            disk_size=DEFAULT_VM_DISK_SIZE,
            enable_api_socket=DEFAULT_VM_ENABLE_API_SOCKET,
            enable_pci=DEFAULT_VM_ENABLE_PCI,
            lsm_flags=DEFAULT_VM_LSM_FLAGS,
        ),
        network=NetworkDefaultsConfig(
            name=DEFAULT_NETWORK_NAME,
            subnet=DEFAULT_NETWORK_SUBNET,
            ipv4_gateway=DEFAULT_NETWORK_IPV4_GATEWAY,
        ),
        paths=PathsConfig(assets_dir=str(get_cache_dir())),
    )
