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
    create_tap,
    delete_tap,
    generate_mac,
    remove_iptables_forward_rules,
)
from fcm.core.network_manager import (
    DEFAULT_NETWORK_NAME,
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from fcm.core.ssh import resolve_ssh_key
from fcm.core.vm_manager import VMManager
from fcm.exceptions import NetworkError, FCMError, VMNotFoundError
from fcm.models.vm import VMConfig, VMInstance, VMState
from fcm.utils.fs import get_kernels_dir, get_images_dir, get_vm_dir
from fcm.constants import TAP_PREFIX


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
        for _ in range(50):
            time.sleep(0.1)
            if not _is_alive(pid):
                break

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
        time.sleep(1.0)

    if _is_alive(pid):
        try:
            os.kill(pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def cleanup_tap(tap_name: str) -> None:
    try:
        remove_iptables_forward_rules(tap_name)
        delete_tap(tap_name)
    except NetworkError:
        pass


def create_vm(
    name: str,
    image: str,
    kernel: str | None = None,
    vcpus: int = 2,
    mem: int = 2048,
    ip: str | None = None,
    network_name: str = DEFAULT_NETWORK_NAME,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    user: str = "root",
    enable_api_socket: bool = False,
    enable_pci: bool = False,
    firecracker_bin: str = "firecracker",
) -> VMInstance:
    import re
    import ipaddress as _ipaddress

    if mac is not None:
        mac_re = re.compile(r"^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$")
        if not mac_re.match(mac):
            raise FCMError(f"Invalid MAC address format: {mac!r}. Expected format: XX:XX:XX:XX:XX:XX")

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
            kernel_path = get_kernels_dir() / "vmlinux"

    if not kernel_path.exists():
        raise FCMError(f"Kernel not found: {kernel_path}")

    fc_bin_path = Path(firecracker_bin)
    if fc_bin_path.is_absolute() or "/" in firecracker_bin:
        if not fc_bin_path.exists():
            raise FCMError(f"Firecracker binary not found: {firecracker_bin}")
        if not os.access(fc_bin_path, os.X_OK):
            raise FCMError(f"Firecracker binary is not executable: {firecracker_bin}")

    image_path: Path
    candidate = get_images_dir() / f"{image}.ext4"
    if candidate.exists():
        image_path = candidate
    else:
        image_path = Path(image)
        if not image_path.exists():
            raise FCMError(f"Image not found: {image!r}")

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
                raise NetworkError(f"IP {ip} is outside network '{network_name}' subnet {net_config.cidr}")
        except ValueError as e:
            raise NetworkError(f"Invalid IP address: {e}")
        guest_ip = ip
    else:
        guest_ip = allocate_network_ip(network_name, name)

    guest_mac = mac if mac else generate_mac()
    tap_name = f"{TAP_PREFIX}-{name}-0"
    bridge = net_config.bridge

    vm_dir.mkdir(parents=True, exist_ok=False)

    rootfs_path = vm_dir / "rootfs.ext4"
    try:
        shutil.copy2(image_path, rootfs_path)
    except OSError as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise FCMError(f"Failed to copy image: {e}")

    cloud_init_dir = vm_dir / "cloud-init"
    cloud_init_dir.mkdir(exist_ok=True)

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
        log_fp = open(log_file, "w")
        console_fp = open(console_log_file, "w")
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

    pid_file.write_text(str(proc.pid))

    manager = VMManager()
    vm_instance = VMInstance(
        name=name,
        pid=proc.pid,
        socket_path=socket_path,
        ip=guest_ip,
        mac=guest_mac,
        network_name=network_name,
        created_at=datetime.now(tz=timezone.utc),
        status=VMState.RUNNING,
    )
    manager.register(vm_instance)
    return vm_instance


def remove_vm(name: str) -> None:
    manager = VMManager()
    vm = manager.get(name)
    if not vm:
        raise VMNotFoundError(f"VM '{name}' not found")

    vm_dir = get_vm_dir(name)
    tap_name = f"{TAP_PREFIX}-{name}-0"

    pid_file = vm_dir / "firecracker.pid"
    pid = int(pid_file.read_text().strip()) if pid_file.exists() else vm.pid

    graceful_shutdown(pid, vm.socket_path)

    remove_iptables_forward_rules(tap_name)
    try:
        delete_tap(tap_name)
    except NetworkError:
        pass

    net_name = vm.network_name or DEFAULT_NETWORK_NAME
    try:
        release_network_ip(net_name, name)
    except NetworkError:
        pass

    if vm.ip:
        try:
            subprocess.run(
                ["ssh-keygen", "-R", vm.ip],
                capture_output=True,
                check=False,
            )
        except FileNotFoundError:
            pass

    manager.deregister(name)

    if vm_dir.exists():
        shutil.rmtree(vm_dir)


def snapshot_vm(name: str, mem_out: Path, state_out: Path) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise FCMError(f"Socket not found for VM '{name}'. Must be running with --enable-api-socket")

    client = FirecrackerClient(socket_path)
    try:
        client.create_snapshot(mem_out, state_out)
    finally:
        client.close()


def load_snapshot(name: str, mem_in: Path, state_in: Path, resume_after: bool = True) -> None:
    socket_path = get_vm_socket_path(name)
    if not socket_path:
        raise FCMError(f"Socket not found for VM '{name}'. Must be running with --enable-api-socket")

    client = FirecrackerClient(socket_path)
    try:
        client.load_snapshot(mem_in, state_in, resume_after)
    finally:
        client.close()
