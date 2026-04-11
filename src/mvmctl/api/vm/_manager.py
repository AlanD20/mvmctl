"""VM lifecycle operations.

This module contains the VMManager class for managing VM lifecycle operations
like start, stop, pause, resume, ssh, logs, etc.
"""

from __future__ import annotations

from pathlib import Path

from mvmctl.constants import (
    CONST_VM_START_WAIT_S,
    DEFAULT_SNAPSHOT_RESUME,
)
from mvmctl.core.console import (
    get_console_state as _get_console_state,
)
from mvmctl.core.firecracker import FirecrackerClient
from mvmctl.core.logs import show_logs
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.ssh import connect_to_vm
from mvmctl.db.models import VMInstance
from mvmctl.exceptions import MVMError
from mvmctl.models import ConsoleInfo, ConsoleState, VMStatus
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.utils.audit import log_audit
from mvmctl.utils.fs import write_pid_file
from mvmctl.utils.process_signals import ProcessSignalHandler


class VMManager:
    """Stateful VM lifecycle manager.

    Resolves VM selector in __init__ and operates on cached VM instance.
    """

    def __init__(self, selector: str, db: MVMDatabase | None = None) -> None:
        """Initialize with VM selector and resolve immediately.

        Args:
            selector: VM name, ID prefix, IP, or MAC address
            db: Optional MVMDatabase instance (creates new if None)

        Raises:
            VMNotFoundError: If VM not found
        """
        from mvmctl.api._internal._resolvers import VMResolver

        self._db = db if db is not None else MVMDatabase()
        resolver = VMResolver()
        self._vm: VMInstance = resolver.resolve(selector)

    def stop(self, force: bool = False) -> None:
        """Stop the VM.

        Args:
            force: If True, force immediate shutdown without graceful shutdown

        Raises:
            MVMError: If VM is not running or stop fails
        """
        name = self._vm.name
        if self._vm.status not in (VMStatus.RUNNING.value, VMStatus.PAUSED.value):
            raise MVMError(f"VM '{name}' is not running (current state: {self._vm.status})")
        self._db.update_vm_status(self._vm.id, VMStatus.STOPPING.value)
        try:
            handler = ProcessSignalHandler(self._vm.pid)

            if not force and self._vm.api_socket_path:
                # Try graceful shutdown via API first
                try:
                    client = FirecrackerClient(Path(self._vm.api_socket_path))
                    client.send_ctrl_alt_del()
                    client.close()
                    # Wait a bit for shutdown
                    import time

                    time.sleep(2.0)
                except Exception:
                    pass  # Fall through to signal-based shutdown

            # Use ProcessSignalHandler for actual shutdown
            if force:
                handler.send_signal(9)  # SIGKILL
            else:
                handler.graceful_shutdown()

            self._db.update_vm_status(self._vm.id, VMStatus.STOPPED.value)
        except Exception as exc:
            self._db.update_vm_status(self._vm.id, VMStatus.ERROR.value)
            raise MVMError(f"Failed to stop VM '{name}': {exc}") from exc

    def pause(self) -> None:
        """Pause the VM.

        Raises:
            MVMError: If VM is not running
        """
        name = self._vm.name
        if self._vm.status != VMStatus.RUNNING.value:
            raise MVMError(f"VM '{name}' is not running (current state: {self._vm.status})")
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")
        client = FirecrackerClient(Path(self._vm.api_socket_path))
        try:
            client.pause_vm()
            self._db.update_vm_status(self._vm.id, VMStatus.PAUSED.value)
        finally:
            client.close()

    def resume(self) -> None:
        """Resume the VM.

        Raises:
            MVMError: If VM is not paused
        """
        name = self._vm.name
        if self._vm.status != VMStatus.PAUSED.value:
            raise MVMError(f"VM '{name}' is not paused (current state: {self._vm.status})")
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")
        client = FirecrackerClient(Path(self._vm.api_socket_path))
        try:
            client.resume_vm()
            self._db.update_vm_status(self._vm.id, VMStatus.RUNNING.value)
        finally:
            client.close()

    def boot(self) -> None:
        """Boot the VM by spawning a new firecracker process.

        Raises:
            MVMError: If VM is not stopped or boot fails
        """
        import subprocess
        import time

        from mvmctl.constants import (
            DEFAULT_FC_CONSOLE_LOG_FILENAME,
            DEFAULT_FC_LOG_FILENAME,
            DEFAULT_FC_PID_FILENAME,
        )

        name = self._vm.name
        if self._vm.status != VMStatus.STOPPED.value:
            raise MVMError(f"VM '{name}' is not stopped (current state: {self._vm.status})")
        if not self._vm.id:
            raise MVMError(f"VM '{name}' has no ID")

        # Use config_path directly from DB
        config_file = Path(self._vm.config_path)
        if not config_file.exists():
            raise MVMError(f"VM config not found: {config_file}")

        # Resolve binary via DB using binary_id
        if not self._vm.binary_id:
            raise MVMError(f"VM '{name}' has no binary assigned")
        binary = self._db.get_binary(self._vm.binary_id)
        if not binary:
            raise MVMError(f"Binary not found for VM '{name}': {self._vm.binary_id}")
        firecracker_bin = binary.path

        # Use api_socket_path directly from DB
        enable_api_socket_runtime = self._vm.enable_api_socket
        socket_path = Path(self._vm.api_socket_path) if self._vm.api_socket_path else None

        if enable_api_socket_runtime and socket_path:
            fc_cmd = [
                firecracker_bin,
                "--api-sock",
                str(socket_path),
                "--config-file",
                str(config_file),
            ]
        else:
            fc_cmd = [firecracker_bin, "--no-api", "--config-file", str(config_file)]

        # Log files are not stored in DB - construct from vm_dir
        # TODO: add these to db table
        vm_dir = config_file.parent
        pid_file = vm_dir / DEFAULT_FC_PID_FILENAME
        log_file = vm_dir / DEFAULT_FC_LOG_FILENAME
        console_log_file = vm_dir / DEFAULT_FC_CONSOLE_LOG_FILENAME

        log_fp = open(log_file, "w", buffering=1, encoding="utf-8")
        console_fp = None
        try:
            console_fp = open(console_log_file, "w", buffering=1, encoding="utf-8")
            proc = subprocess.Popen(
                fc_cmd,
                stdin=subprocess.DEVNULL,
                stdout=console_fp,
                stderr=log_fp,
                start_new_session=True,
            )
            log_fp.close()
            console_fp.close()
            write_pid_file(pid_file, proc.pid)
            self._db.update_vm_pid(self._vm.id, proc.pid)
            self._db.update_vm_status(self._vm.id, VMStatus.RUNNING.value)
            time.sleep(CONST_VM_START_WAIT_S)
        except Exception as exc:
            try:
                log_fp.close()
            except OSError:
                pass
            if console_fp is not None:
                try:
                    console_fp.close()
                except OSError:
                    pass
            raise MVMError(f"Failed to boot VM '{name}': {exc}") from exc

    def start(self) -> None:
        """Start an already configured VM via Firecracker API.

        Raises:
            MVMError: If VM is not stopped or start fails
        """
        name = self._vm.name
        if self._vm.status != VMStatus.STOPPED.value:
            raise MVMError(f"VM '{name}' is not stopped (current state: {self._vm.status})")
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")

        client = FirecrackerClient(Path(self._vm.api_socket_path))
        try:
            client.start_instance()
            self._db.update_vm_status(self._vm.id, VMStatus.RUNNING.value)
        finally:
            client.close()

    def reboot(self, force: bool = False) -> None:
        """Reboot the VM (stop then boot).

        Args:
            force: If True, force immediate shutdown

        Raises:
            MVMError: If reboot fails
        """
        self.stop(force=force)
        self.start()

    def snapshot(self, mem_out: Path, state_out: Path) -> None:
        """Snapshot VM memory and disk state.

        Args:
            mem_out: Memory snapshot output path
            state_out: VM state output path

        Raises:
            MVMError: If socket not found or snapshot fails
        """
        if not self._vm.api_socket_path:
            raise MVMError(
                f"Socket not found for VM '{self._vm.name}'. Must be running with --enable-api-socket"
            )
        socket_path = Path(self._vm.api_socket_path)
        client = FirecrackerClient(socket_path)
        try:
            client.create_snapshot(mem_out, state_out)
        finally:
            client.close()

    def load_snapshot(self, mem_in: Path, state_in: Path, resume_after: bool | None = None) -> None:
        """Load VM from snapshot.

        Args:
            mem_in: Memory snapshot input path
            state_in: VM state input path
            resume_after: Whether to resume VM after loading

        Raises:
            MVMError: If socket not found or load fails
        """
        effective_resume = resume_after if resume_after is not None else DEFAULT_SNAPSHOT_RESUME
        if not self._vm.api_socket_path:
            raise MVMError(
                f"Socket not found for VM '{self._vm.name}'. Must be running with --enable-api-socket"
            )
        socket_path = Path(self._vm.api_socket_path)
        client = FirecrackerClient(socket_path)
        try:
            client.load_snapshot(mem_in, state_in, effective_resume)
        finally:
            client.close()

    def ssh(self, user: str, key: Path | None = None, cmd: str | None = None) -> int:
        """Open SSH session or execute command on the VM.

        Args:
            user: SSH user
            key: Path to SSH private key
            cmd: Optional command to execute

        Returns:
            Exit code from SSH session

        Raises:
            MVMError: If VM has no IP address
        """
        if not self._vm.ipv4:
            raise MVMError(f"VM '{self._vm.name}' has no IP address")

        log_audit("vm.ssh", f"name={self._vm.name},user={user}")

        return connect_to_vm(
            ip=self._vm.ipv4,
            user=user,
            key_path=key,
            command=cmd,
            exec_mode=cmd is None,
        )

    def get_logs(self, log_type: str, lines: int, follow: bool) -> list[str]:
        """View VM logs.

        Args:
            log_type: Type of log (boot or os)
            lines: Number of lines to show
            follow: Whether to follow log output

        Returns:
            List of log lines
        """
        vm_hash = self._vm.id if self._vm.id else self._vm.name
        return show_logs(
            vm_hash=vm_hash,
            log_type=log_type,
            lines=lines,
            follow=follow,
        )

    def attach_console(self) -> ConsoleInfo:
        """Attach to the VM's console relay.

        Returns:
            ConsoleInfo containing the socket path and VM name.

        Raises:
            MVMError: If no console relay is running for the VM.
        """
        mgr = ConsoleRelayManager()
        name = self._vm.name
        vm_hash = self._vm.id if self._vm.id else None
        if not mgr.is_relay_running(name, vm_hash):
            raise MVMError(f"No console relay running for VM '{name}'")

        socket_path_str = mgr.get_socket_path(vm_hash if vm_hash else name)
        return ConsoleInfo(socket_path=Path(socket_path_str), vm_name=name)

    def kill_console(self) -> bool:
        """Kill the console relay for the VM.

        Returns:
            True if relay was killed, False otherwise
        """
        mgr = ConsoleRelayManager()
        vm_hash = self._vm.id if self._vm.id else None
        return mgr.kill_relay(self._vm.name, vm_hash)

    def get_console_state(self) -> ConsoleState:
        """Get the current console state for the VM.

        Returns:
            ConsoleState containing the relay status, PID, and socket path.
        """
        vm_hash = self._vm.id if self._vm.id else None
        state = _get_console_state(self._vm.name, vm_hash)
        return ConsoleState(
            running=state.get("running", False),
            pid=state.get("pid"),
            socket_path=state.get("socket_path"),
        )


__all__ = ["VMManager"]
