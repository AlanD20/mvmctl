from __future__ import annotations

import http.client
import json
import logging
import socket
import subprocess
import time
from pathlib import Path
from typing import Any, Collection, NotRequired, TextIO, TypedDict, override

from mvmctl.constants import (
    CONST_HTTP_STATUS_NO_CONTENT,
    CONST_HTTP_STATUS_SUCCESS,
    CONST_POLL_STEP_SECONDS,
    CONST_SOCKET_TIMEOUT_SECONDS,
    DEFAULT_FC_API_SOCKET_FILENAME,
    DEFAULT_FC_CONFIG_FILENAME,
    DEFAULT_FC_LOG_FILENAME,
    DEFAULT_FC_LOG_LEVEL,
    DEFAULT_FC_METRICS_FILENAME,
    DEFAULT_FC_PID_FILENAME,
    DEFAULT_FC_SERIAL_OUTPUT_FILENAME,
    DEFAULT_LIBGUESTFS_SEED_DIR,
)
from mvmctl.exceptions import (
    FirecrackerClientError,
    FirecrackerConfigError,
    FirecrackerSpawnError,
    SocketNotFoundError,
)
from mvmctl.models.cloudinit import CloudInitMode
from mvmctl.models.firecracker import FirecrackerConfig
from mvmctl.utils.fs import write_pid_file

logger = logging.getLogger(__name__)


class BootSourceConfig(TypedDict):
    boot_args: str
    kernel_image_path: str
    initrd_path: NotRequired[str | None]


class DriveConfig(TypedDict):
    drive_id: str
    path_on_host: str
    is_root_device: bool
    is_read_only: bool
    partuuid: NotRequired[str]
    cache_type: str
    io_engine: str
    rate_limiter: NotRequired[object | None]
    socket: NotRequired[str | None]


class NetworkInterfaceConfig(TypedDict):
    iface_id: str
    guest_mac: str
    host_dev_name: str


class MachineConfig(TypedDict):
    vcpu_count: int
    mem_size_mib: int
    smt: bool
    track_dirty_pages: bool
    cpu_template: NotRequired[str | None]


class LoggerConfig(TypedDict):
    log_path: str
    level: str
    show_level: bool
    show_log_origin: bool


class MetricsConfig(TypedDict):
    metrics_path: str


FirecrackerConfigDict = TypedDict(
    "FirecrackerConfigDict",
    {
        "boot-source": BootSourceConfig,
        "drives": list[DriveConfig],
        "network-interfaces": list[NetworkInterfaceConfig],
        "machine-config": MachineConfig,
        "balloon": NotRequired[object | None],
        "vsock": NotRequired[object | None],
        "logger": NotRequired[LoggerConfig | None],
        "metrics": NotRequired[MetricsConfig | None],
    },
)


class FirecrackerSpawner:
    """Manage Firecracker."""

    pid: int | None = None
    process_start_time: int | None = None
    fc_log_fp: TextIO | None = None
    serial_output_fp: TextIO | None = None

    def __init__(
        self,
        config: FirecrackerConfig,
        *,
        config_path: Path | None = None,
    ):
        self._config = config
        self._config_path = (
            config_path
            if config_path
            else config.vm_dir / DEFAULT_FC_CONFIG_FILENAME
        )
        self._log_path = config.vm_dir / DEFAULT_FC_LOG_FILENAME
        self._metrics_path = config.vm_dir / DEFAULT_FC_METRICS_FILENAME
        self._serial_output_path = (
            config.vm_dir / DEFAULT_FC_SERIAL_OUTPUT_FILENAME
        )
        self._pid_path = config.vm_dir / DEFAULT_FC_PID_FILENAME
        self._api_socket_path = config.vm_dir / DEFAULT_FC_API_SOCKET_FILENAME

    @property
    def log_path(self) -> Path:
        return self._log_path

    @property
    def api_socket_path(self) -> Path:
        return self._api_socket_path

    @property
    def pid_path(self) -> Path:
        return self._pid_path

    @property
    def serial_output_path(self) -> Path:
        return self._serial_output_path

    @property
    def metrics_path(self) -> Path:
        return self._metrics_path

    @property
    def config_path(self) -> Path:
        return self._config_path

    def spawn(
        self,
        *,
        relay_enabled: bool = False,
        relay_client_fd: int | None = None,
    ) -> None:

        fc_stdin = subprocess.DEVNULL
        fc_stdout = self.serial_output_fp
        fc_pass_fds: Collection[int] = []

        if self._config.enable_console and relay_enabled:
            if relay_client_fd is None:
                raise FirecrackerSpawnError(
                    "Console enabled but PTY client FD is None"
                )

            fc_stdin = relay_client_fd
            fc_stdout = relay_client_fd
            fc_pass_fds = [relay_client_fd]
        else:
            self.serial_output_fp = self.create_filepointer(
                self._serial_output_path
            )

        fc_proc: subprocess.Popen[Any] | None = None
        self.fc_log_fp = self.create_filepointer(self._log_path)

        fc_proc = subprocess.Popen(
            [
                self._config.binary_path,
                "--api-sock",
                str(self._api_socket_path),
                "--config-file",
                str(self._config_path),
            ],
            stdin=fc_stdin,
            stdout=fc_stdout,
            stderr=self.fc_log_fp,
            start_new_session=True,
            pass_fds=fc_pass_fds,
        )

        time.sleep(CONST_POLL_STEP_SECONDS)
        poll_result = fc_proc.poll()

        if poll_result is not None and isinstance(poll_result, int):
            raise FirecrackerSpawnError(
                f"Firecracker process exited immediately with code {poll_result}"
            )

        # Close file pointers since the firecracker process is managing them
        self._close_filepointers()

        self.pid = fc_proc.pid
        from mvmctl.utils._system import ProcessSignalHandler

        self.process_start_time = ProcessSignalHandler._get_process_start_time(
            fc_proc.pid
        )
        write_pid_file(self._pid_path, fc_proc.pid)

    def cleanup(self) -> None:
        """Perform cleanup of all created resources."""

        self._close_filepointers()

    def generate(self) -> FirecrackerConfigDict:
        # Build as regular dict to allow dynamic optional keys
        config: dict[str, Any] = {
            "boot-source": BootSourceConfig(
                kernel_image_path=self._config.kernel_path,
                boot_args=self._build_boot_args(),
            ),
            "drives": self._build_drives_config(),
            "network-interfaces": self._build_network_config(),
            "machine-config": MachineConfig(
                vcpu_count=self._config.vcpu_count,
                mem_size_mib=self._config.mem_size_mib,
                smt=False,
                track_dirty_pages=False,
            ),
        }

        if self._config.enable_logging:
            config["logger"] = self._build_logger_config()

        if self._config.enable_metrics:
            config["metrics"] = self._build_metrics_config()

        return config  # type: ignore[return-value]

    def _build_drives_config(self) -> list[DriveConfig]:
        drives: list[DriveConfig] = [
            {
                "drive_id": "rootfs",
                "path_on_host": str(self._config.rootfs_path.absolute()),
                "is_root_device": True,
                "is_read_only": False,
                "cache_type": "Unsafe",
                "io_engine": "Sync",
            }
        ]

        # Cloud-init ISO drive (if configured)
        if (
            self._config.cloud_init_mode is not None
            and self._config.cloud_init_mode != CloudInitMode.OFF
            and self._config.cloud_init_iso_path is not None
        ):
            cloud_init_drive: DriveConfig = {
                "drive_id": "cloud-init",
                "path_on_host": str(self._config.cloud_init_iso_path),
                "is_root_device": False,
                "is_read_only": True,
                "cache_type": "Unsafe",
                "io_engine": "Sync",
            }
            drives.append(cloud_init_drive)

        # Extra drives
        # IMPROVEMENTS: Allow adding extra drives to be mounted, maybe introduce volumes?

        return drives

    def _build_logger_config(self) -> LoggerConfig:
        logger: LoggerConfig = {
            "log_path": str(self._log_path),
            "level": DEFAULT_FC_LOG_LEVEL,
            "show_level": True,
            "show_log_origin": True,
        }

        return logger

    def _build_metrics_config(self) -> MetricsConfig:
        metric: MetricsConfig = {
            "metrics_path": str(self._metrics_path),
        }

        return metric

    def _build_boot_args(self) -> str:

        boot_args = {}

        if self._config.boot_args is not None:
            boot_args = self._parse_boot_args_to_dict(self._config.boot_args)

        if not self._config.enable_pci:
            self._set_boot_arg(boot_args, "pci", "off")

        # Use static kernel ip= parameter for early network bringup
        # This ensures network is ready before cloud-init runs
        # For NO_CLOUD_NET mode, also include kernel ip= for initial network bringup
        # cloud-init's network-config will ensure the IP stays consistent

        self._set_boot_arg(
            boot_args,
            "ip",
            f"{self._config.guest_ip}::{self._config.network_gateway}:{self._config.network_netmask}::eth0:off",
        )

        if self._config.lsm_flags:
            self._set_boot_arg(boot_args, "lsm", self._config.lsm_flags)

        if self._config.image_fs_uuid:
            self._set_boot_arg(
                boot_args, "root", f"UUID={self._config.image_fs_uuid}"
            )
        else:
            self._set_boot_arg(boot_args, "root", "/dev/vda")

        if self._config.image_fs_uuid:
            self._set_boot_arg(
                boot_args, "rootfstype", self._config.image_fs_type
            )

        # Determine cloud-init datasource string
        # Don't handle CloudInitMode.OFF since we don't have to add any boot args
        if (
            self._config.cloud_init_mode is not None
            and self._config.cloud_init_mode != CloudInitMode.OFF
        ):
            # Mask systemd-networkd-wait-online to prevent 2+ minute boot delay
            # The kernel ip= parameter already configures the network; this service
            # would block waiting for systemd-networkd to mark it as "online"
            self._set_boot_arg(
                boot_args,
                "systemd.mask",
                "systemd-networkd-wait-online.service",
            )
            if self._config.cloud_init_mode == CloudInitMode.NET:
                # For nocloud-net, validate URL is configured
                if not self._config.cloud_init_nocloud_url:
                    raise FirecrackerConfigError(
                        "NoCloud URL must be set when using NET mode, pos"
                    )
                self._set_boot_arg(
                    boot_args,
                    "ds",
                    f"nocloud;seedfrom={self._config.cloud_init_nocloud_url}",
                )
            elif self._config.cloud_init_mode == CloudInitMode.INJECT:
                self._set_boot_arg(
                    boot_args,
                    "ds",
                    f"ds=nocloud;s=file://{DEFAULT_LIBGUESTFS_SEED_DIR}/",
                )
            elif self._config.cloud_init_mode == CloudInitMode.ISO:
                # ISO mode: local nocloud datasource
                self._set_boot_arg(boot_args, "ds", "nocloud")

        return self._join_boot_args_dict(boot_args)

    def _parse_boot_args_to_dict(
        self, boot_args: str
    ) -> dict[str, list[str] | None]:
        """Parse boot arguments string into a dictionary with list values.

        Handles kernel-style boot arguments in format 'key=value' or flags.
        Multiple occurrences of the same key are stored as a list of values.
        Multiple spaces between arguments are normalized.

        Args:
            boot_args: Space-separated boot arguments (e.g., "pci=off quiet root=/dev/vda")

        Returns:
            Dictionary mapping argument keys to lists of values. Single values are
            stored as one-element lists. Flags without values are mapped to None.
            (e.g., {"pci": ["off"], "quiet": None, "systemd.mask": ["s1", "s2"]})

        Examples:
            >>> self._parse_boot_args_to_dict("pci=off")
            {"pci": ["off"]}
            >>> self._parse_boot_args_to_dict("pci=off quiet splash")
            {"pci": ["off"], "quiet": None, "splash": None}
            >>> self._parse_boot_args_to_dict("systemd.mask=s1 systemd.mask=s2")
            {"systemd.mask": ["s1", "s2"]}
        """
        result: dict[str, list[str] | None] = {}
        if not boot_args or not boot_args.strip():
            return result

        args = [arg.strip() for arg in boot_args.split() if arg.strip()]

        for arg in args:
            if "=" in arg:
                key, value = arg.split("=", 1)
                existing = result.get(key)
                if existing is None:
                    result[key] = [value]
                else:
                    existing.append(value)
            else:
                result[arg] = None

        return result

    def _join_boot_args_dict(
        self, boot_args_dict: dict[str, list[str] | None]
    ) -> str:
        """Join boot arguments dictionary back into a space-separated string.

        Reverses _parse_boot_args_to_dict(). Handles both key=value pairs and
        flags (keys with None values). For list values, duplicates the key
        for each value to support multiple arguments with the same key.

        Args:
            boot_args_dict: Dictionary mapping keys to lists of values (None for flags).

        Returns:
            Space-separated boot arguments string.

        Examples:
            >>> self._join_boot_args_dict({"pci": ["off"], "quiet": None, "root": ["/dev/vda"]})
            "pci=off quiet root=/dev/vda"
            >>> self._join_boot_args_dict({"systemd.mask": ["s1", "s2"]})
            "systemd.mask=s1 systemd.mask=s2"
        """
        parts: list[str] = []
        for key, values in boot_args_dict.items():
            if values is None:
                parts.append(key)
            else:
                for value in values:
                    parts.append(f"{key}={value}")
        return " ".join(parts)

    def _set_boot_arg(
        self, boot_args_dict: dict[str, list[str] | None], key: str, value: str
    ) -> None:
        """Set (overwrite) a boot argument value in the dictionary.

        If the key exists, its value is replaced.  This ensures that
        arguments such as ``root=`` or ``pci=`` only appear once in the
        generated command line.

        Args:
            boot_args_dict: The boot arguments dictionary to modify.
            key: The boot argument key (e.g., "pci", "systemd.mask").
            value: The value to set.
        """
        boot_args_dict[key] = [value]

    def _build_network_config(self) -> list[NetworkInterfaceConfig]:

        networks: list[NetworkInterfaceConfig] = [
            {
                "iface_id": "eth0",
                "guest_mac": self._config.guest_mac,
                "host_dev_name": self._config.tap_name,
            }
        ]

        # Extra networks
        # IMPROVEMENTS: Allow adding extra interfaces.

        return networks

    def write_to_file(self) -> None:

        config = self.generate()
        self._config_path.parent.mkdir(parents=True, exist_ok=True)
        with open(self._config_path, "w") as f:
            json.dump(config, f)

    def create_filepointer(self, path: Path):
        return open(path, "w", buffering=1, encoding="utf-8")

    def _close_filepointers(self) -> None:
        try:
            if self.fc_log_fp is not None:
                self.fc_log_fp.close()
                self.fc_log_fp = None

            if self.serial_output_fp is not None:
                self.serial_output_fp.close()
                self.serial_output_fp = None

        except OSError as exc:
            logger.warning("Failed to close filepointer(s): %s", exc)


class InstanceInfo(TypedDict):
    """Instance info returned from Firecracker get_instance_info()."""

    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    boot_time: str | None


class InstanceDescription(TypedDict):
    """Instance description returned from Firecracker describe_instance()."""

    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    flags: list[str]
    if_addr: dict[str, str]
    used_block_devices: list[str]


class UnixSocketHTTPConnection(http.client.HTTPConnection):
    """HTTP connection over Unix domain socket."""

    def __init__(self, socket_path: Path):
        self._socket_path = socket_path
        super().__init__("localhost")

    @override
    def connect(self) -> None:
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(CONST_SOCKET_TIMEOUT_SECONDS)
        self.sock.connect(str(self._socket_path))


class FirecrackerClient:
    """Firecracker API client."""

    def __init__(self, socket_path: Path):
        self._socket_path = Path(socket_path)
        self._conn: UnixSocketHTTPConnection | None = None

    def __enter__(self) -> FirecrackerClient:
        """Connect to Firecracker socket and return client."""
        self._connect()
        return self

    def __exit__(
        self,
        _exc_type: type[BaseException] | None,
        _exc_val: BaseException | None,
        _exc_tb: object,
    ) -> None:
        """Close the socket connection."""
        self.close()

    def _connect(self) -> None:
        """Connect to Firecracker socket.

        Raises:
            SocketNotFoundError: If the socket file does not exist.
            FirecrackerError: If connection to the socket fails.
        """
        if not self._socket_path.exists():
            raise SocketNotFoundError(f"Socket not found: {self._socket_path}")

        try:
            self._conn = UnixSocketHTTPConnection(self._socket_path)
        except OSError as e:
            raise FirecrackerClientError(
                f"Failed to connect to socket: {e}"
            ) from e

    def _request(
        self,
        method: str,
        path: str,
        body: dict[str, object] | None = None,
    ) -> tuple[int, dict[str, object] | None]:
        """Make HTTP request to Firecracker API.

        Raises:
            SocketNotFoundError: If the socket file does not exist.
            FirecrackerError: If the API request fails.
        """
        if not self._conn:
            self._connect()
        assert self._conn is not None

        headers = {"Content-Type": "application/json"} if body else {}
        body_json = json.dumps(body) if body else None

        try:
            self._conn.request(method, path, body=body_json, headers=headers)
            response = self._conn.getresponse()
            status = response.status

            # Read response body
            response_body = response.read().decode("utf-8")
            data = json.loads(response_body) if response_body else None

            return status, data

        except (SocketNotFoundError, FirecrackerClientError):
            raise
        except OSError as e:
            raise FirecrackerClientError(f"API request failed: {e}") from e

    def close(self) -> None:
        """Close connection."""
        if self._conn:
            self._conn.close()
            self._conn = None

    def create_snapshot(
        self,
        mem_path: Path,
        snapshot_path: Path,
    ) -> bool:
        """Create VM snapshot.

        Args:
            mem_path: Path to save memory state
            snapshot_path: Path to save VM state

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If snapshot creation fails.
        """
        logger.info("Creating snapshot...")

        body: dict[str, object] = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
        }

        status, data = self._request("PUT", "/snapshot/create", body)

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Snapshot created")
            logger.info("  Memory: %s", mem_path)
            logger.info("  State: %s", snapshot_path)
            return True
        else:
            msg = f"Failed to create snapshot: {status}"
            if data:
                msg += f" Response: {data}"
            raise FirecrackerClientError(msg)

    def load_snapshot(
        self,
        mem_path: Path,
        snapshot_path: Path,
        resume: bool = True,
    ) -> bool:
        """Load VM from snapshot.

        Args:
            mem_path: Path to memory state file
            snapshot_path: Path to VM state file
            resume: Whether to resume VM after loading

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If snapshot loading fails.
        """
        logger.info("Loading snapshot...")

        body = {
            "mem_file_path": str(mem_path),
            "snapshot_path": str(snapshot_path),
            "resume_vm": resume,
        }

        status, data = self._request("PUT", "/snapshot/load", body)

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Snapshot loaded")
            return True
        else:
            msg = f"Failed to load snapshot: {status}"
            if data:
                msg += f" Response: {data}"
            raise FirecrackerClientError(msg)

    def get_instance_info(self) -> InstanceInfo | None:
        """Get VM instance information.

        Returns:
            InstanceInfo TypedDict or None
        """
        status, data = self._request("GET", "/")

        if status == CONST_HTTP_STATUS_SUCCESS and data:
            return data  # type: ignore[return-value]
        return None

    def describe_instance(self) -> InstanceDescription | None:
        """Describe the VM instance.

        Returns:
            InstanceDescription TypedDict or None
        """
        status, data = self._request("GET", "/vm")

        if status == CONST_HTTP_STATUS_SUCCESS and data:
            return data  # type: ignore[return-value]
        return None

    def start_instance(self) -> bool:
        """Start the VM instance.

        Returns:
            True if successful.

        Raises:
            FirecrackerError: If the start operation fails.
        """
        logger.info("Starting VM...")
        status, _ = self._request(
            "PUT", "/actions", {"action_type": "InstanceStart"}
        )

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM started")
            return True
        else:
            raise FirecrackerClientError(f"Failed to start VM: {status}")

    def send_ctrl_alt_del(self) -> bool:
        """Send Ctrl+Alt+Del to VM.

        Returns:
            True if successful, False otherwise
        """
        try:
            status, _ = self._request(
                "PUT", "/actions", {"action_type": "SendCtrlAltDel"}
            )
        except (SocketNotFoundError, FirecrackerClientError):
            logger.error("Failed to send Ctrl+Alt+Del")
            return False

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("Ctrl+Alt+Del sent")
            return True
        else:
            logger.error("Failed to send Ctrl+Alt+Del: %s", status)
            return False

    def pause_vm(self) -> None:
        """Pause the microVM via PATCH /vm.

        Raises:
            FirecrackerError: If the pause operation fails.
        """
        logger.info("Pausing VM...")
        status, _ = self._request("PATCH", "/vm", {"state": "Paused"})

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM paused")
        else:
            raise FirecrackerClientError(f"Failed to pause VM: {status}")

    def resume_vm(self) -> None:
        """Resume a paused microVM via PATCH /vm.

        Raises:
            FirecrackerError: If the resume operation fails.
        """
        logger.info("Resuming VM...")
        status, _ = self._request("PATCH", "/vm", {"state": "Resumed"})

        if status == CONST_HTTP_STATUS_NO_CONTENT:
            logger.info("VM resumed")
        else:
            raise FirecrackerClientError(f"Failed to resume VM: {status}")
