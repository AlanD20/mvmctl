"""Registry - orchestration functions for VM lifecycle."""

from __future__ import annotations

import logging
import os
import signal
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api._internal._resolvers._key_resolver import resolve_default_public_keys
from mvmctl.api.vm._creation import (
    CloudInitProvisioner,
    GuestfsProvisioner,
    VMCreationContext,
)
from mvmctl.api.vm._creation_resolver import VMCreationResolver
from mvmctl.api.vm._spawn import spawn_firecracker_vm
from mvmctl.constants import (
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    CONST_VM_VCPU_MAX,
    CONST_VM_VCPU_MIN,
    DEFAULT_FC_CONFIG_FILENAME,
    MAX_VMS,
)
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.core.firewall import (
    add_nocloud_input_rule,
    setup_nocloud_input_chain,
)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.network import (
    add_iptables_forward_rules,
    bridge_exists,
    create_tap,
    generate_mac,
    setup_bridge,
    setup_nat,
)
from mvmctl.core.vm_lifecycle import _secure_mkdir_vm
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.exceptions import (
    CloudInitError,
    MVMError,
    NetworkError,
    VMCreateError,
)
from mvmctl.models.vm import VMConfig, VMInstance, VMStatus
from mvmctl.utils.audit import log_audit
from mvmctl.utils.disk_size import parse_disk_size
from mvmctl.utils.fs import get_vm_dir_by_hash
from mvmctl.utils.network import generate_tap_name

if TYPE_CHECKING:
    from mvmctl.models.vm import VMCreateInput

logger = logging.getLogger(__name__)


def create_vm(input: VMCreateInput, vm_manager: VMManager | None = None) -> VMInstance:
    """Create a new VM from the provided input configuration.

    This is the orchestrator function that coordinates all components
    for VM creation using the class-based architecture.

    Args:
        input: VM creation input containing all configuration parameters.
        vm_manager: Optional VM manager instance for dependency injection.

    Returns:
        The created VM instance.

    Raises:
        AssetNotFoundError: If no image is specified and no default image is set.
        MVMError: If VM limits are exceeded or validation fails.
        VMCreateError: If VM creation fails.
        NetworkError: If network setup fails.
        CloudInitError: If cloud-init configuration fails.
    """
    from mvmctl.api.host import check_privileges_interactive
    from mvmctl.api.network import allocate_network_ip, get_network
    from mvmctl.core.image import copy_from_ready_pool, ensure_image_in_ready_pool
    from mvmctl.models.cloud_init import CloudInitMode

    check_privileges_interactive("/usr/sbin/ip", f"create VM '{input.name}'")

    resolver = VMCreationResolver()
    resolved = resolver.resolve(input)

    import re

    if resolved.mac is not None:
        mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not mac_re.match(resolved.mac):
            raise MVMError(
                f"Invalid MAC address format: {resolved.mac!r}. Expected format: XX:XX:XX:XX:XX:XX"
            )

    manager = vm_manager or get_vm_manager()
    if manager.count_vms() >= MAX_VMS:
        raise MVMError(
            f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
        )

    if not (CONST_VM_VCPU_MIN <= resolved.vcpus <= CONST_VM_VCPU_MAX):
        raise MVMError(
            f"Invalid vcpus={resolved.vcpus}: must be between {CONST_VM_VCPU_MIN} and {CONST_VM_VCPU_MAX}"
        )
    if not (CONST_VM_MEM_MIN_MIB <= resolved.mem <= CONST_VM_MEM_MAX_MIB):
        raise MVMError(f"Invalid mem_size_mib={resolved.mem}: must be between 128 and 65536")

    if not resolved.kernel_path.exists():
        raise MVMError(f"Kernel not found: {resolved.kernel_path}")

    fc_bin_path = Path(resolved.firecracker_bin)
    if (fc_bin_path.is_absolute() or "/" in resolved.firecracker_bin) and (
        not fc_bin_path.exists() or not os.access(fc_bin_path, os.X_OK)
    ):
        raise MVMError(f"Firecracker binary not found: {resolved.firecracker_bin}")

    if resolved.user_data is not None and not resolved.user_data.exists():
        raise MVMError(f"User-data file not found: {resolved.user_data}")

    net_config = get_network(resolved.network_name)
    if net_config is None:
        raise NetworkError(f"Network '{resolved.network_name}' not found")

    setup_nocloud_input_chain()

    vm_id = _generate_vm_id(input.name)
    resolved.vm_id = vm_id

    ctx = VMCreationContext(resolved=resolved)

    vm_dir = get_vm_dir_by_hash(vm_id)
    ctx.vm_dir = vm_dir
    _secure_mkdir_vm(vm_dir, input.name)
    ctx.mark_created("vm_dir")

    guest_mac = resolved.mac if resolved.mac else generate_mac()
    tap_name = generate_tap_name(resolved.network_name, input.name)
    ctx.tap_name = tap_name

    if resolved.ip:
        import ipaddress as ipaddress_module

        ip_net = ipaddress_module.IPv4Network(net_config.subnet, strict=False)
        if ipaddress_module.IPv4Address(resolved.ip.split("/")[0]) not in ip_net:
            raise NetworkError(
                f"IP {resolved.ip} is outside network '{resolved.network_name}' subnet {net_config.subnet}"
            )
        guest_ip = resolved.ip
    else:
        guest_ip = allocate_network_ip(resolved.network_name, vm_id)
        ctx.mark_created("network_ip")

    ctx.guest_ip = guest_ip

    db = MVMDatabase()
    image_entry = None
    if resolved.image_hash:
        image_entry = db.get_image(resolved.image_hash)
    elif input.image:
        image_entry = db.get_image_by_os_slug(input.image)

    if image_entry is None or image_entry.minimum_rootfs_size_mib is None:
        image_id = input.image or resolved.image_hash or str(resolved.image_path)
        os_slug = image_entry.os_slug if image_entry else input.image or "unknown"
        raise VMCreateError(
            f"Image {image_id} is missing minimum_rootfs_size_mib. "
            f"This image was created with an older version. "
            f"Re-import the image: mvm image fetch {os_slug} --force"
        )

    min_size_mb = image_entry.minimum_rootfs_size_mib

    if resolved.disk_size is not None:
        requested_bytes = parse_disk_size(resolved.disk_size)
        from mvmctl.constants import CONST_MEBIBYTE_BYTES

        min_required_bytes = min_size_mb * CONST_MEBIBYTE_BYTES
        if requested_bytes < min_required_bytes:
            raise VMCreateError(
                f"Requested disk size ({resolved.disk_size}) is smaller than "
                f"minimum required ({min_size_mb} MiB). "
                f"Use a larger size or choose a different image."
            )

    if resolved.image_path.suffix == ".zst":
        rootfs_ext = resolved.image_path.suffixes[-2]
        vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
        fs_type = rootfs_ext.lstrip(".")
        if resolved.image_hash is None:
            raise MVMError(f"image_hash required for compressed images: {resolved.image_path}")
        ensure_image_in_ready_pool(resolved.image_path, resolved.image_hash, fs_type)
        copy_from_ready_pool(resolved.image_hash, fs_type, vm_rootfs_path)
    else:
        rootfs_ext = resolved.image_path.suffix
        vm_rootfs_path = vm_dir / f"rootfs{rootfs_ext}"
        import shutil

        shutil.copy2(resolved.image_path, vm_rootfs_path)

    target_size = parse_disk_size(resolved.disk_size) if resolved.disk_size is not None else None

    if resolved.cloud_init_mode == CloudInitMode.OFF:
        ssh_keys = resolve_default_public_keys(resolved.ssh_key)
        provisioner = GuestfsProvisioner(
            rootfs_path=vm_rootfs_path,
            hostname=input.name,
            user=resolved.user,
            ssh_pub_key=ssh_keys,
        )
        provisioner.provision(target_size_bytes=target_size)
    elif target_size is not None:
        provisioner = GuestfsProvisioner(
            rootfs_path=vm_rootfs_path,
            hostname=input.name,
            user=resolved.user,
            ssh_pub_key=None,
        )
        provisioner.provision(target_size_bytes=target_size)

    if resolved.cloud_init_mode != CloudInitMode.OFF:
        ssh_pub_key = resolve_default_public_keys(resolved.ssh_key)
        cloud_init_provisioner = CloudInitProvisioner()
        ctx.cloud_init_result = cloud_init_provisioner.provision(
            mode=resolved.cloud_init_mode,
            vm_dir=vm_dir,
            guest_ip=guest_ip,
            user=resolved.user,
            ssh_pub_key=ssh_pub_key,
            user_data=resolved.user_data,
            net_config=net_config,
            vm_id=vm_id,
            nocloud_net_port=resolved.nocloud_net_port if resolved.nocloud_net_port else None,
            cloud_init_iso_path=resolved.cloud_init_iso_path,
            keep_cloud_init_iso=resolved.keep_cloud_init_iso,
        )

        if ctx.cloud_init_result.nocloud_url:
            ctx.nocloud_net_port = ctx.cloud_init_result.nocloud_port
            ctx.mark_created("nocloud_server")
            add_nocloud_input_rule(guest_ip, input.name, ctx.nocloud_net_port)
            ctx.mark_created("firewall_rule")

    from mvmctl.utils.network import subnet_mask_from_subnet

    subnet_mask = subnet_mask_from_subnet(net_config.subnet)

    disk_size_mib = (
        (parse_disk_size(resolved.disk_size) // (1024 * 1024))
        if resolved.disk_size is not None
        else min_size_mb
    )

    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME

    vm_config = VMConfig(
        name=input.name,
        vm_id=vm_id,
        vcpu_count=resolved.vcpus,
        mem_size_mib=resolved.mem,
        disk_size_mib=disk_size_mib,
        kernel_path=resolved.kernel_path,
        rootfs_path=vm_rootfs_path,
        root_uuid=resolved.image_fs_uuid,
        root_fs_type=resolved.image_fs_type,
        enable_api_socket=resolved.enable_api_socket,
        enable_pci=resolved.enable_pci,
        lsm_flags=resolved.lsm_flags,
        enable_logging=resolved.enable_logging,
        enable_metrics=resolved.enable_metrics,
        enable_console=resolved.enable_console,
        cloud_init_mode=resolved.cloud_init_mode,
        cloud_init_iso_path=ctx.cloud_init_result.iso_path if ctx.cloud_init_result else None,
        keep_cloud_init_iso=resolved.keep_cloud_init_iso,
        nocloud_net_url=ctx.cloud_init_result.nocloud_url if ctx.cloud_init_result else None,
        extra_drives=[],
    )

    now = datetime.now(tz=timezone.utc)

    if image_entry is not None:
        resolved_image_id = image_entry.id
    else:
        resolved_image_id = resolved.image_hash or str(resolved.image_path)

    kernel_entry = None
    if input.kernel:
        kernel_entry = db.get_kernel_by_name(input.kernel)
    if kernel_entry is None:
        kernel_entry = db.get_default_kernel()
    if kernel_entry is not None:
        resolved_kernel_id = kernel_entry.id
    else:
        resolved_kernel_id = str(resolved.kernel_path)

    if resolved.binary_id:
        resolved_binary_id = resolved.binary_id
    else:
        binary_entry = db.get_default_binary("firecracker")
        if binary_entry is not None:
            resolved_binary_id = binary_entry.id
        else:
            resolved_binary_id = ""

    vm_instance = VMInstance(
        name=input.name,
        id=vm_id,
        pid=0,
        ipv4=guest_ip,
        mac=guest_mac,
        network_id=resolved.network_id,
        tap_device=tap_name,
        ipv4_gateway=net_config.ipv4_gateway,
        subnet_mask=subnet_mask,
        created_at=now,
        updated_at=now,
        status=VMStatus.RUNNING,
        config=vm_config,
        config_path=config_file,
        rootfs_suffix=rootfs_ext,
        kernel_id=resolved_kernel_id,
        image_id=resolved_image_id,
        binary_id=resolved_binary_id,
        disk_size_mib=disk_size_mib,
    )

    if vm_config.boot_args:
        from mvmctl.utils.validation import validate_boot_arg_component

        for component in vm_config.boot_args.split():
            validate_boot_arg_component(component, "boot_args")
    if vm_config.root_uuid:
        from mvmctl.utils.validation import validate_fs_uuid

        validate_fs_uuid(vm_config.root_uuid, "root_uuid")
    if vm_config.root_fs_type:
        from mvmctl.utils.validation import validate_fs_type

        validate_fs_type(vm_config.root_fs_type, "root_fs_type")
    if vm_instance.ipv4:
        from mvmctl.utils.validation import validate_boot_arg_component

        validate_boot_arg_component(vm_instance.ipv4, "guest_ip")
    if vm_instance.ipv4_gateway:
        from mvmctl.utils.validation import validate_boot_arg_component

        validate_boot_arg_component(vm_instance.ipv4_gateway, "ipv4_gateway")
    if vm_instance.subnet_mask:
        from mvmctl.utils.validation import validate_boot_arg_component

        validate_boot_arg_component(vm_instance.subnet_mask, "subnet_mask")
    if vm_config.lsm_flags:
        from mvmctl.utils.validation import validate_boot_arg_component

        validate_boot_arg_component(vm_config.lsm_flags, "lsm_flags")

    ConfigGenerator(vm_config, vm_instance, vm_dir).write_to_file(config_file)

    if resolved.enable_console:
        ctx.pty_master_fd, ctx.pty_slave_fd = os.openpty()
        from mvmctl.services.console_relay.manager import ConsoleRelayManager

        ctx.relay_mgr = ConsoleRelayManager()

    bridge = net_config.bridge
    if not bridge_exists(bridge):
        import ipaddress as ipaddress_module

        gateway_cidr = (
            f"{net_config.ipv4_gateway}/"
            f"{ipaddress_module.IPv4Network(net_config.subnet, strict=False).prefixlen}"
        )
        setup_bridge(bridge, ipv4_gateway_subnet=gateway_cidr)
        if net_config.nat_enabled:
            setup_nat(
                bridge,
                nat_gateways=net_config.nat_gateways or None,
                subnet=net_config.subnet,
            )

    try:
        create_tap(tap_name, bridge=bridge)
        ctx.mark_created("tap")
        add_iptables_forward_rules(tap_name, bridge=bridge)
    except NetworkError as exc:
        raise NetworkError(f"Network setup failed: {exc}") from exc

    old_handler = signal.signal(signal.SIGTERM, lambda signum, frame: _sigterm_handler(ctx, signum))

    try:
        try:
            pid, api_socket, console_relay_pid = spawn_firecracker_vm(ctx, resolved, config_file)

            vm_instance.pid = pid
            vm_instance.api_socket_path = api_socket
            vm_instance.nocloud_net_port = (
                ctx.cloud_init_result.nocloud_port if ctx.cloud_init_result else None
            )
            vm_instance.nocloud_server_pid = (
                ctx.cloud_init_result.nocloud_pid if ctx.cloud_init_result else None
            )
            vm_instance.console_relay_pid = console_relay_pid

            if console_relay_pid:
                from mvmctl.constants import DEFAULT_CONSOLE_SOCKET_FILENAME

                vm_instance.console_socket_path = vm_dir / DEFAULT_CONSOLE_SOCKET_FILENAME

            manager.register(vm_instance)
            ctx.mark_created("vm_instance")

            log_audit("vm.create", f"name={input.name}")

            return vm_instance

        except (VMCreateError, NetworkError, CloudInitError, MVMError):
            if input.skip_cleanup:
                ctx.persist_failed_vm(vm_instance, manager)
            else:
                ctx.cleanup()
            raise
        except FileNotFoundError as exc:
            if input.skip_cleanup:
                ctx.persist_failed_vm(vm_instance, manager)
            else:
                ctx.cleanup()
            raise MVMError(f"Firecracker binary not found: {resolved.firecracker_bin}") from exc
        except Exception as exc:
            if input.skip_cleanup:
                ctx.persist_failed_vm(vm_instance, manager)
            else:
                ctx.cleanup()
            raise VMCreateError(f"Failed to create VM: {exc}") from exc
    finally:
        signal.signal(signal.SIGTERM, old_handler)


def _generate_vm_id(name: str) -> str:
    """Generate unique VM ID from name and timestamp."""
    import hashlib
    import time

    timestamp = str(time.time())
    hash_input = f"{name}:{timestamp}"
    full_hash = hashlib.sha256(hash_input.encode()).hexdigest()
    return full_hash[:16]


def _sigterm_handler(ctx: VMCreationContext, signum: int) -> None:
    """Handle SIGTERM during VM creation."""
    ctx.cleanup()
    from mvmctl.constants import CONST_SIGNAL_EXIT_CODE_BASE

    raise SystemExit(CONST_SIGNAL_EXIT_CODE_BASE + signum)


__all__ = ["create_vm"]
