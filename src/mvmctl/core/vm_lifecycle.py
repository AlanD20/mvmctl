import fcntl
import hashlib
import logging
import os
import random
import shutil
import signal
import string
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.constants import (
    BRIDGE_NAME,
    CLI_NAME,
    CONST_DIR_PERMS_CACHE,
    CONST_FILE_PERMS_PID_FILE,
    CONST_POLL_STEP_SECONDS,
    CONST_VM_MEM_MAX_MIB,
    CONST_VM_MEM_MIN_MIB,
    DEFAULT_CLOUD_INIT_DIRNAME,
    DEFAULT_FC_API_SOCKET_FILENAME,
    DEFAULT_FC_CONFIG_FILENAME,
    DEFAULT_FC_CONSOLE_LOG_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_NETWORK_NAME,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_VM_KERNEL_FILENAME,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_ROOTFS_BASENAME,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_VCPU_COUNT,
    FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S,
    FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S,
    FIRECRACKER_SIGTERM_WAIT_S,
    MAX_VMS,
    SUPPORTED_IMAGE_EXTENSIONS,
)
from mvmctl.core.cloud_init import inject_cloud_init, write_cloud_init
from mvmctl.core.config_gen import ConfigGenerator
from mvmctl.core.firecracker import FirecrackerClient, get_vm_socket_path
from mvmctl.core.network import (
    add_iptables_forward_rules,
    bridge_exists,
    create_tap,
    delete_tap,
    generate_mac,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
)
from mvmctl.core.network_manager import (
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from mvmctl.core.ssh import resolve_ssh_key
from mvmctl.core.vm_manager import VMManager, get_vm_manager
from mvmctl.exceptions import MVMError, NetworkError, VMNotFoundError
from mvmctl.models.vm import VMConfig, VMInstance, VMState
from mvmctl.utils.fs import get_images_dir, get_kernels_dir, get_vm_dir


def _resolve_image_path(image: str) -> Path:
    images_dir = get_images_dir()

    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        candidate = images_dir / f"{image}{ext}"
        if candidate.exists():
            return candidate

    direct = Path(image)
    if direct.is_absolute() and direct.exists():
        return direct

    from mvmctl.core.metadata import find_images_by_short_id
    from mvmctl.utils.fs import get_cache_dir

    matches = find_images_by_short_id(get_cache_dir(), image)
    if len(matches) == 1:
        full_key, meta = matches[0]
        filename = str(meta.get("filename", ""))
        if filename:
            candidate = images_dir / filename
            if candidate.exists():
                return candidate
        for ext in SUPPORTED_IMAGE_EXTENSIONS:
            candidate = images_dir / f"{full_key}{ext}"
            if candidate.exists():
                return candidate

    if direct.exists():
        return direct

    raise MVMError(f"Image not found: {image!r}")


def generate_vm_id(name: str) -> str:
    """Generate a unique VM ID from name and current time."""
    data = f"{name}:{time.time()}"
    return hashlib.sha256(data.encode()).hexdigest()


logger = logging.getLogger(__name__)


def _generate_tap_name(network_name: str, vm_name: str) -> str:
    rand_suffix = "".join(random.choices(string.ascii_lowercase, k=3))
    net_part = network_name[:3]
    vm_part = vm_name[:3]
    return f"{CLI_NAME}-{net_part}-{vm_part}-{rand_suffix}"


def _write_pid_file(pid_file: Path, pid: int) -> None:
    """Write PID to file with an exclusive advisory lock."""
    fd = os.open(str(pid_file), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, CONST_FILE_PERMS_PID_FILE)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        os.write(fd, str(pid).encode())
    finally:
        fcntl.flock(fd, fcntl.LOCK_UN)
        os.close(fd)


def _read_pid_file(pid_file: Path) -> int | None:
    """Read PID from file and verify the process actually exists."""
    if not pid_file.exists():
        return None
    try:
        pid = int(pid_file.read_text().strip())
    except (ValueError, OSError):
        return None
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return None
    except PermissionError:
        pass  # process exists but we can't signal it
    return pid


def _secure_mkdir_vm(vm_dir: Path, name: str) -> None:
    """Atomically create VM directory with TOCTOU protection.

    Uses atomic mkdir with symlink detection to prevent race conditions
    where an attacker creates a symlink between check and create.

    Args:
        vm_dir: Path to the VM directory to create
        name: VM name for error messages

    Raises:
        MVMError: If directory exists, is a symlink, or race condition detected
    """
    # SECURITY: Use os.lstat() to detect symlinks before attempting creation
    # This prevents the TOCTOU race between check and mkdir
    try:
        # Check if path exists and is a symlink BEFORE attempting creation
        os.lstat(vm_dir)  # Raises FileNotFoundError if path doesn't exist
        if os.path.islink(vm_dir):
            raise MVMError(f"VM '{name}' path is a symlink (possible attack): {vm_dir}")
        raise MVMError(f"VM '{name}' already exists at {vm_dir}")
    except FileNotFoundError:
        # Expected - path doesn't exist, safe to proceed with atomic mkdir
        pass

    # SECURITY: Attempt atomic directory creation
    # exist_ok=False ensures we fail if path was created between check and mkdir
    try:
        vm_dir.mkdir(parents=True, exist_ok=False)
    except FileExistsError:
        # Race condition: path created between our check and mkdir
        # Re-verify to detect symlinks
        if os.path.islink(vm_dir):
            raise MVMError(f"VM '{name}' path is a symlink (race condition detected): {vm_dir}")
        raise MVMError(f"VM '{name}' already exists at {vm_dir}")

    # SECURITY: Verify the created directory is not a symlink
    # This catches cases where mkdir followed a symlink to a different parent
    if os.path.islink(vm_dir):
        # Attempt cleanup - but the symlink attack may have already succeeded
        # We can't safely clean up, just report the security issue
        raise MVMError(f"VM '{name}' directory is a symlink (security violation): {vm_dir}")


def graceful_shutdown(pid: int | None, socket_path: Path | None) -> None:
    if pid is None:
        return

    def _is_alive(p: int) -> bool:
        try:
            os.kill(p, 0)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    if socket_path is not None and Path(socket_path).exists():
        try:
            client = FirecrackerClient(Path(socket_path))
            client.send_ctrl_alt_del()
            client.close()
        except (ProcessLookupError, PermissionError, InterruptedError):
            pass
        # Poll for FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S seconds (100ms steps)
        # to allow graceful shutdown before SIGTERM/SIGKILL.
        _poll_steps = int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S / CONST_POLL_STEP_SECONDS)
        for _ in range(_poll_steps):
            time.sleep(FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S)
            # P-L3: single check per iteration — no fix needed
            if not _is_alive(pid):
                break

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        time.sleep(float(FIRECRACKER_SIGTERM_WAIT_S))

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def cleanup_tap(tap_name: str, bridge: str | None = None) -> None:
    try:
        remove_iptables_forward_rules(tap_name, bridge=bridge or BRIDGE_NAME)
        delete_tap(tap_name)
    except NetworkError:
        pass


def create_vm(
    name: str,
    image: str,
    kernel: str | None = None,
    vcpus: int = DEFAULT_VM_VCPU_COUNT,
    mem: int = DEFAULT_VM_MEM_MIB,
    ip: str | None = None,
    network_name: str = DEFAULT_NETWORK_NAME,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    user: str = DEFAULT_VM_SSH_USER,
    enable_api_socket: bool = DEFAULT_VM_ENABLE_API_SOCKET,
    enable_pci: bool = DEFAULT_VM_ENABLE_PCI,
    firecracker_bin: str = DEFAULT_FIRECRACKER_BIN_NAME,
    vm_manager: VMManager | None = None,
) -> VMInstance:
    import ipaddress as _ipaddress
    import re

    from mvmctl.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")

    manager = vm_manager or get_vm_manager()
    if manager.count_vms() >= MAX_VMS:
        raise MVMError(
            f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
        )

    if not (1 <= vcpus <= 32):
        raise MVMError(f"Invalid vcpus={vcpus}: must be between 1 and 32")
    if not (CONST_VM_MEM_MIN_MIB <= mem <= CONST_VM_MEM_MAX_MIB):
        raise MVMError(f"Invalid mem_size_mib={mem}: must be between 128 and 65536")

    if mac is not None:
        mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not mac_re.match(mac):
            raise MVMError(
                f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX"
            )

    vm_dir = get_vm_dir(name)
    _secure_mkdir_vm(vm_dir, name)

    kernel_path: Path
    if kernel:
        kernel_path = Path(kernel)
    else:
        env_kernel = os.environ.get("MVM_KERNEL")
        if env_kernel:
            kernel_path = Path(env_kernel)
        else:
            kernel_path = get_kernels_dir() / DEFAULT_VM_KERNEL_FILENAME

    if not kernel_path.exists():
        raise MVMError(f"Kernel not found: {kernel_path}")

    fc_bin_path = Path(firecracker_bin)
    if fc_bin_path.is_absolute() or "/" in firecracker_bin:
        if not fc_bin_path.exists():
            raise MVMError(f"Firecracker binary not found: {firecracker_bin}")
        if not os.access(fc_bin_path, os.X_OK):
            raise MVMError(f"Firecracker binary is not executable: {firecracker_bin}")

    image_path = _resolve_image_path(image)

    if user_data is not None and not user_data.exists():
        raise MVMError(f"User-data file not found: {user_data}")

    net_config = get_network(network_name)
    if net_config is None:
        if network_name == DEFAULT_NETWORK_NAME:
            net_config = ensure_default_network()
        else:
            raise NetworkError(f"Network '{network_name}' not found")

    if ip:
        try:
            ip_net = _ipaddress.IPv4Network(net_config.cidr, strict=False)
            if _ipaddress.IPv4Address(ip.split("/")[0]) not in ip_net:
                raise NetworkError(
                    f"IP {ip} is outside network '{network_name}' subnet {net_config.cidr}"
                )
        except ValueError as e:
            raise NetworkError(f"Invalid IP address: {e}")
        guest_ip = ip
    else:
        guest_ip = allocate_network_ip(network_name, name)

    # P4 §3: use random MAC (02:FC:XX:XX:XX:XX prefix) — intentionally
    # overrides P3's deterministic-from-name scheme per spec precedence rule.
    guest_mac = mac if mac else generate_mac()
    tap_name = _generate_tap_name(network_name, name)
    bridge = net_config.bridge

    rootfs_ext = image_path.suffix
    rootfs_path = vm_dir / f"{DEFAULT_VM_ROOTFS_BASENAME}{rootfs_ext}"
    try:
        shutil.copy2(image_path, rootfs_path)
    except OSError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise MVMError(f"Failed to copy image: {e}")

    cloud_init_dir = vm_dir / DEFAULT_CLOUD_INIT_DIRNAME
    cloud_init_dir.mkdir(mode=CONST_DIR_PERMS_CACHE, exist_ok=True)

    ssh_pub_key = resolve_ssh_key(ssh_key)

    _prefix_len = _ipaddress.IPv4Network(net_config.cidr, strict=False).prefixlen

    write_cloud_init(
        cloud_init_dir,
        name,
        guest_ip,
        user,
        ssh_pub_key=ssh_pub_key,
        custom_user_data=user_data,
        gateway=net_config.gateway,
        prefix_len=_prefix_len,
    )
    inject_cloud_init(rootfs_path, cloud_init_dir)

    socket_path = vm_dir / DEFAULT_FC_API_SOCKET_FILENAME if enable_api_socket else None
    _net = _ipaddress.IPv4Network(net_config.cidr, strict=False)
    _subnet_mask = str(_net.netmask)

    vm_config = VMConfig(
        name=name,
        vcpu_count=vcpus,
        mem_size_mib=mem,
        kernel_path=kernel_path,
        rootfs_path=rootfs_path,
        guest_ip=guest_ip,
        guest_mac=guest_mac,
        gateway=net_config.gateway,
        subnet_mask=_subnet_mask,
        tap_device=tap_name,
        enable_api_socket=enable_api_socket,
        enable_pci=enable_pci,
    )
    config_file = vm_dir / DEFAULT_FC_CONFIG_FILENAME
    ConfigGenerator(vm_config).write_to_file(config_file)

    # AUDIT-4: Reconcile bridge if it has drifted (e.g. lost after reboot).
    if not bridge_exists(bridge):
        logger.info("Bridge %s not found — recreating for network '%s'", bridge, network_name)
        _gw_cidr = (
            f"{net_config.gateway}"
            f"/{_ipaddress.IPv4Network(net_config.cidr, strict=False).prefixlen}"
        )
        setup_bridge(bridge, gateway_cidr=_gw_cidr)
        if net_config.nat_enabled:
            setup_nat(bridge)

    try:
        create_tap(tap_name, bridge=bridge)
        add_iptables_forward_rules(tap_name, bridge=bridge)
    except NetworkError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        try:
            release_network_ip(network_name, name)
        except NetworkError:
            pass
        raise NetworkError(f"Network setup failed: {e}")

    log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
    console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME
    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME

    fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]
    if enable_api_socket and socket_path:
        fc_cmd = [
            firecracker_bin,
            "--api-sock",
            str(socket_path),
            "--config-file",
            str(config_file),
        ]

    try:
        log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
        console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
        proc = subprocess.Popen(
            fc_cmd,
            stdin=subprocess.DEVNULL,
            stdout=console_fp,
            stderr=log_fp,
            start_new_session=True,
        )
    except FileNotFoundError:
        cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise MVMError(f"Firecracker binary not found: {firecracker_bin!r}")
    except OSError as e:
        cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise MVMError(f"Failed to start Firecracker: {e}")

    _write_pid_file(pid_file, proc.pid)

    vm_instance = VMInstance(
        name=name,
        id=generate_vm_id(name),
        pid=proc.pid,
        socket_path=socket_path,
        ip=guest_ip,
        mac=guest_mac,
        network_name=network_name,
        tap_device=tap_name,
        created_at=datetime.now(tz=timezone.utc),
        status=VMState.RUNNING,
    )
    manager.register(vm_instance)
    return vm_instance


def remove_vm(name: str, vm_manager: VMManager | None = None) -> None:
    manager = vm_manager or get_vm_manager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir(name)
    net_name = vm.network_name or DEFAULT_NETWORK_NAME
    tap_name = vm.tap_device or _generate_tap_name(net_name, name)

    net_config = get_network(net_name)
    bridge = net_config.bridge if net_config else BRIDGE_NAME

    pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
    pid = _read_pid_file(pid_file)
    if pid is None:
        pid = vm.pid

    graceful_shutdown(pid, vm.socket_path)

    remove_iptables_forward_rules(tap_name, bridge=bridge)
    try:
        delete_tap(tap_name)
    except NetworkError:
        pass

    try:
        release_network_ip(net_name, name)
    except NetworkError as e:
        logger.warning("Failed to release network IP: %s", e)

    if vm.ip:
        try:
            subprocess.run(
                ["ssh-keygen", "-R", vm.ip],
                capture_output=True,
                check=False,
            )
        except FileNotFoundError:
            pass

    manager.deregister(vm.id)

    if vm_dir.exists():
        shutil.rmtree(vm_dir)


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )

    client = FirecrackerClient(socket_path)
    try:
        client.create_snapshot(mem_out, state_out)
    finally:
        client.close()


def load_snapshot(name: str, mem_in: Path, state_in: Path, resume_after: bool = True) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise MVMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )

    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, resume_after)
    finally:
        client.close()
