"""Console relay session management for CLI use.

Provides ConsoleRelaySession class that encapsulates all console operations
for CLI console access - from VM resolution to socket I/O.
"""

import select
import socket
from collections.abc import Generator
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api._internal._resolvers import VMResolver
from mvmctl.constants import CONST_CONSOLE_SOCKET_TIMEOUT_S
from mvmctl.exceptions import MVMError
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.services.console_relay._defaults import CONST_CONSOLE_SELECT_TIMEOUT_S

if TYPE_CHECKING:
    from mvmctl.db.models import VMInstance
    from mvmctl.models import ConsoleInfo


class ConsoleRelaySession:
    """Complete console session for a VM.

    Encapsulates VM resolution, relay connection, input/output handling,
    and escape sequence detection for CLI console sessions.
    """

    def __init__(
        self,
        name: str,
        buffer_size: int = 4096,
    ) -> None:
        """Initialize console session for a VM.

        Args:
            name: VM name or ID prefix
            buffer_size: Read buffer size
        """
        self._name = name
        self._buffer_size = buffer_size
        self._sock: socket.socket | None = None
        self._relay: ConsoleRelayManager | None = None
        self._vm: VMInstance | None = None

    def _resolve_vm(self) -> "VMInstance":
        """Resolve VM by name."""
        if self._vm is None:
            resolver = VMResolver()
            self._vm = resolver.resolve(self._name)
        return self._vm

    def _get_relay(self) -> ConsoleRelayManager:
        """Get or create relay manager for VM."""
        if self._relay is None:
            vm = self._resolve_vm()
            vm_dir = Path(vm.config_path).parent if vm.config_path else Path()
            self._relay = ConsoleRelayManager(
                id=vm.id,
                path=vm_dir,
                name=vm.name,
            )
        return self._relay

    def attach(self) -> "ConsoleInfo":
        """Attach to VM console - resolves VM and returns connection info.

        Returns:
            ConsoleInfo with socket_path for connection

        Raises:
            VMNotFoundError: If VM not found
            MVMError: If console relay is not running
        """
        from mvmctl.models import ConsoleInfo

        vm = self._resolve_vm()
        relay = self._get_relay()

        if not relay.is_running():
            raise MVMError(f"No console relay running for VM '{self._name}'")

        return ConsoleInfo(
            socket_path=relay.socket_path(),
            vm_name=vm.name,
        )

    def get_state(self) -> dict:
        """Get console state for the VM.

        Returns:
            Dict with: running (bool), pid (int|None), socket_path (str)
        """
        relay = self._get_relay()
        return {
            "running": relay.is_running(),
            "pid": relay.get_pid(),
            "socket_path": str(relay.socket_path()),
        }

    def kill(self) -> bool:
        """Kill the console relay for the VM.

        Returns:
            True if relay was stopped, False if not running
        """
        relay = self._get_relay()
        if not relay.is_running():
            return False
        return relay.terminate()

    def connect(self) -> None:
        """Connect to console relay Unix socket.

        Raises:
            ConnectionRefusedError: If relay not accepting connections
            FileNotFoundError: If socket doesn't exist
            TimeoutError: If connection times out
            MVMError: If relay not running
        """
        relay = self._get_relay()
        if not relay.is_running():
            raise MVMError(f"No console relay running for VM '{self._name}'")

        self._sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self._sock.settimeout(CONST_CONSOLE_SOCKET_TIMEOUT_S)
        self._sock.connect(str(relay.socket_path()))
        self._sock.setblocking(False)

    def disconnect(self) -> None:
        """Disconnect from relay socket."""
        if self._sock:
            try:
                self._sock.close()
            except OSError:
                pass
            self._sock = None

    def send(self, data: bytes) -> bool:
        """Send input to console.

        Args:
            data: Bytes to send

        Returns:
            True if successful, False if connection broken
        """
        if not self._sock or not data:
            return True
        try:
            self._sock.sendall(data)
            return True
        except (OSError, BrokenPipeError, ConnectionResetError):
            return False

    def receive(self) -> Generator[bytes, None, None]:
        """Receive output from console.

        Yields:
            Bytes from console
        """
        if not self._sock:
            return

        while True:
            ready, _, _ = select.select(
                [self._sock.fileno()], [], [], CONST_CONSOLE_SELECT_TIMEOUT_S
            )
            if self._sock.fileno() in ready:
                try:
                    data = self._sock.recv(self._buffer_size)
                    if data:
                        yield data
                    else:
                        return
                except (BlockingIOError, InterruptedError):
                    continue
                except (OSError, ConnectionResetError):
                    return

    def check_escape_sequence(
        self, buffer: bytearray, sequence: bytes = b"\x18d"
    ) -> tuple[bool, str]:
        """Check if buffer ends with escape sequence.

        Args:
            buffer: Input buffer
            sequence: Escape sequence to check (default: Ctrl+X then 'd')

        Returns:
            Tuple of (matched, action) where action is "detach" if matched
        """
        if len(buffer) >= len(sequence):
            matched = bytes(buffer[-len(sequence) :]) == sequence
            return matched, "detach" if matched else ""
        return False, ""

    def interact(self) -> None:
        """Run interactive console session.

        Connects to relay and runs the main I/O loop, handling:
        - Reading output from VM and writing to stdout
        - Reading input from stdin and sending to VM
        - Escape sequence detection for detach

        This method blocks until detached or connection closed.

        Raises:
            RuntimeError: If not connected
        """
        import sys

        if self._sock is None:
            raise RuntimeError("Not connected - call connect() first")

        input_buffer = bytearray()
        detach_requested = False
        running = True

        while running:
            ready, _, _ = select.select([sys.stdin, self._sock], [], [], 0.05)

            if self._sock in ready:
                try:
                    data = self._sock.recv(self._buffer_size)
                    if data:
                        sys.stdout.buffer.write(data)
                        sys.stdout.flush()
                    else:
                        running = False
                except BlockingIOError:
                    pass
                except (OSError, ConnectionResetError):
                    running = False

            if sys.stdin in ready:
                char = sys.stdin.buffer.read(1)
                if not char:
                    running = False
                    continue

                input_buffer.extend(char)
                matched, action = self.check_escape_sequence(input_buffer)
                if matched and action == "detach":
                    detach_requested = True
                    running = False
                    continue

                if input_buffer[0:1] != b"\x18":
                    to_send = bytes(input_buffer)
                    if to_send:
                        self.send(to_send)
                    input_buffer = bytearray()
                elif len(input_buffer) >= 2:
                    if input_buffer != b"\x18d":
                        to_send = bytes(input_buffer)
                        if to_send:
                            self.send(to_send)
                        input_buffer = bytearray()

        if detach_requested:
            if input_buffer:
                self.send(bytes(input_buffer[:-2]))

    def __enter__(self) -> "ConsoleRelaySession":
        """Context manager entry - connects automatically."""
        self.connect()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        """Context manager exit - disconnects automatically."""
        self.disconnect()
