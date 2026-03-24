import fcntl
import hashlib
import logging
import os
import shutil
import signal
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path

from fcm.core.cloud_init import write_cloud_init, inject_cloud_init
from fcm.core.config_gen import ConfigGenerator
from fcm.core.firecracker import FirecrackerClient, get_vm_socket_path
from fcm.core.network import (
    add_iptables_forward_rules,
    bridge_exists,
    create_tap,
    delete_tap,
    generate_mac,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
    teardown_nat,
)
from fcm.core.network_manager import (
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from fcm.core.ssh import resolve_ssh_key
from fcm.core.vm_manager import VMManager, get_vm_manager
from fcm.exceptions import NetworkError, FCMError, VMNotFoundError
from fcm.models.vm import VMConfig, VMInstance, VMState
from fcm.utils.fs import get_kernels_dir, get_images_dir, get_vm_dir
import random
import string

from fcm.constants import (
    BRIDGE_NAME,
    DEFAULT_NETWORK_NAME,
    FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S,
    FIRECRACKER_SIGTERM_WAIT_S,
    MAX_VMS,
    CLI_NAME,
    DEFAULT_VM_VCPU_COUNT,
    DEFAULT_VM_MEM_MIB,
    DEFAULT_VM_SSH_USER,
    DEFAULT_VM_ENABLE_API_SOCKET,
    DEFAULT_VM_ENABLE_PCI,
    DEFAULT_FIRECRACKER_BIN_NAME,
    DEFAULT_VM_KERNEL_FILENAME,
    SUPPORTED_IMAGE_EXTENSIONS,
)


def _resolve_image_path(image: str) -> Path:
    images_dir = get_images_dir()

    for ext in SUPPORTED_IMAGE_EXTENSIONS:
        candidate = images_dir / f"{image}{ext}"
        if candidate.exists():
            return candidate

    direct = Path(image)
    if direct.is_absolute() and direct.exists():
        return direct

    from fcm.core.metadata import find_images_by_short_id
    from fcm.utils.fs import get_cache_dir

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

    raise FCMError(f"Image not found: {image!r}")


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
    fd = os.open(str(pid_file), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
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
        _poll_steps = int(FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S / 0.1)
        for _ in range(_poll_steps):
            time.sleep(0.1)
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
    import re
    import ipaddress as _ipaddress
    from fcm.utils.validation import validate_entity_name

    validate_entity_name(name, "VM")

    manager = vm_manager or get_vm_manager()
    existing_vms = manager.list_all()
    if len(existing_vms) >= MAX_VMS:
        raise FCMError(
            f"VM limit reached ({MAX_VMS}). Remove existing VMs before creating new ones."
        )

    if not (1 <= vcpus <= 32):
        raise FCMError(f"Invalid vcpus={vcpus}: must be between 1 and 32")
    if not (128 <= mem <= 65536):
        raise FCMError(f"Invalid mem_size_mib={mem}: must be between 128 and 65536")

    if mac is not None:
        mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not mac_re.match(mac):
            raise FCMError(
                f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX"
            )

    vm_dir = get_vm_dir(name)
    if vm_dir.exists():
        raise FCMError(f"VM '{name}' already exists at {vm_dir}")

    kernel_path: Path
    if kernel:
        kernel_path = Path(kernel)
    else:
        env_kernel = os.environ.get("FCM_KERNEL")
        if env_kernel:
            kernel_path = Path(env_kernel)
        else:
            kernel_path = get_kernels_dir() / DEFAULT_VM_KERNEL_FILENAME

    if not kernel_path.exists():
        raise FCMError(f"Kernel not found: {kernel_path}")

    fc_bin_path = Path(firecracker_bin)
    if fc_bin_path.is_absolute() or "/" in firecracker_bin:
        if not fc_bin_path.exists():
            raise FCMError(f"Firecracker binary not found: {firecracker_bin}")
        if not os.access(fc_bin_path, os.X_OK):
            raise FCMError(f"Firecracker binary is not executable: {firecracker_bin}")

    image_path = _resolve_image_path(image)

    if user_data is not None and not user_data.exists():
        raise FCMError(f"User-data file not found: {user_data}")

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

    vm_dir.mkdir(parents=True, exist_ok=False)

    rootfs_path = vm_dir / "rootfs.ext4"
    try:
        shutil.copy2(image_path, rootfs_path)
    except OSError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise FCMError(f"Failed to copy image: {e}")

    cloud_init_dir = vm_dir / "cloud-init"
    cloud_init_dir.mkdir(mode=0o700, exist_ok=True)

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

    socket_path = vm_dir / "firecracker.api.socket" if enable_api_socket else None
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
    config_file = vm_dir / "firecracker.json"
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

    log_file = vm_dir / "firecracker.log"
    console_log_file = vm_dir / "firecracker.console.log"
    pid_file = vm_dir / "firecracker.pid"

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
        with open(log_file, "w") as log_fp, open(console_log_file, "w") as console_fp:
            proc = subprocess.Popen(
                fc_cmd,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )
    except FileNotFoundError:
        cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise FCMError(f"Firecracker binary not found: {firecracker_bin!r}")
    except OSError as e:
        cleanup_tap(tap_name)
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise FCMError(f"Failed to start Firecracker: {e}")

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

    pid_file = vm_dir / "firecracker.pid"
    pid = _read_pid_file(pid_file)
    if pid is None:
        pid = vm.pid

    graceful_shutdown(pid, vm.socket_path)

    remove_iptables_forward_rules(tap_name, bridge=bridge)
    try:
        delete_tap(tap_name)
    except NetworkError:
        pass

    if net_config and net_config.nat_enabled:
        try:
            teardown_nat(bridge=bridge, force=False)
        except NetworkError as e:
            logger.warning("Failed to teardown NAT for bridge %s: %s", bridge, e)

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
        raise FCMError(
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
        raise FCMError(
            f"Socket not found for VM '{name}'. Must be running with --enable-api-socket"
        )

    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, resume_after)
    finally:
        client.close()
