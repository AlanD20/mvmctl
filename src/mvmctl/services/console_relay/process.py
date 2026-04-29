"""Console relay standalone process.

Reads from PTY master file descriptor, writes to console.log file,
and forwards to a Unix socket for CLI attachment.
"""

import argparse
import os
import select
import signal
import socket
import sys
from pathlib import Path

_shutdown_state = {"requested": False}


def _signal_handler(signum: int, _frame: object) -> None:
    _shutdown_state["requested"] = True


def _setup_signal_handlers() -> None:
    signal.signal(signal.SIGTERM, _signal_handler)
    signal.signal(signal.SIGINT, _signal_handler)


def _write_pid_file(pid_file: Path) -> None:
    pid_file.parent.mkdir(parents=True, exist_ok=True)
    pid_file.write_text(str(os.getpid()))


def _cleanup_pid_file(pid_file: Path) -> None:
    try:
        if pid_file.exists():
            pid_file.unlink()
    except OSError:
        pass


def _cleanup_socket(sock_path: Path) -> None:
    try:
        if sock_path.exists():
            sock_path.unlink()
    except OSError:
        pass


def _read_from_pty(pty_fd: int, buffer_size: int) -> bytes:
    try:
        return os.read(pty_fd, buffer_size)
    except OSError:
        return b""


def _write_to_log(log_file: Path, data: bytes) -> None:
    try:
        with open(log_file, "ab") as f:
            f.write(data)
            f.flush()
    except OSError:
        pass


def _accept_client(server_sock: socket.socket) -> socket.socket | None:
    try:
        server_sock.setblocking(False)
        client_sock, _ = server_sock.accept()
        return client_sock
    except (BlockingIOError, OSError):
        return None


def _forward_to_client(client_sock: socket.socket | None, data: bytes) -> bool:
    if client_sock is None:
        return True
    try:
        client_sock.sendall(data)
        return True
    except (OSError, BrokenPipeError, ConnectionResetError):
        return False


def _read_from_client(client_sock: socket.socket | None) -> bytes:
    if client_sock is None:
        return b""
    try:
        client_sock.setblocking(False)
        return client_sock.recv(4096)
    except (BlockingIOError, OSError):
        return b""


def _forward_to_pty(pty_fd: int, data: bytes) -> bool:
    if not data:
        return True
    try:
        os.write(pty_fd, data)
        return True
    except OSError:
        return False


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Console relay process for serial console"
    )
    parser.add_argument("--id", required=True, help="Unique identifier")
    parser.add_argument("--name", required=True, help="Name for logging")
    parser.add_argument(
        "--pty-controller-fd",
        type=int,
        required=True,
        help="PTY controller file descriptor",
    )
    parser.add_argument(
        "--socket-path", type=Path, required=True, help="Unix socket path"
    )
    parser.add_argument(
        "--pid-file", type=Path, required=True, help="PID file path"
    )
    parser.add_argument(
        "--log-file", type=Path, required=True, help="Console log file path"
    )
    from mvmctl.services.console_relay._defaults import (
        CONST_CONSOLE_READ_BUFFER_SIZE,
    )

    parser.add_argument(
        "--buffer-size",
        type=int,
        default=CONST_CONSOLE_READ_BUFFER_SIZE,
        help="Read buffer size",
    )
    args = parser.parse_args()

    _setup_signal_handlers()
    _write_pid_file(args.pid_file)

    pty_fd = args.pty_controller_fd
    sock_path = args.socket_path
    log_file = args.log_file
    buffer_size = args.buffer_size

    server_sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    _cleanup_socket(sock_path)
    server_sock.bind(str(sock_path))
    server_sock.listen(1)

    client_sock = None

    try:
        while not _shutdown_state["requested"]:
            readable_fds = [pty_fd, server_sock.fileno()]
            if client_sock is not None:
                readable_fds.append(client_sock.fileno())

            ready, _, _ = select.select(readable_fds, [], [], 0.1)

            for fd in ready:
                if fd == pty_fd:
                    data = _read_from_pty(pty_fd, buffer_size)
                    if not data:
                        _shutdown_state["requested"] = True
                        break
                    _write_to_log(log_file, data)
                    if (
                        not _forward_to_client(client_sock, data)
                        and client_sock is not None
                    ):
                        try:
                            client_sock.close()
                        except OSError:
                            pass
                        client_sock = None

                elif fd == server_sock.fileno():
                    if client_sock is None:
                        new_client = _accept_client(server_sock)
                        if new_client is not None:
                            client_sock = new_client

                elif client_sock is not None and fd == client_sock.fileno():
                    input_data = _read_from_client(client_sock)
                    if input_data:
                        _forward_to_pty(pty_fd, input_data)
                    else:
                        try:
                            client_sock.close()
                        except OSError:
                            pass
                        client_sock = None

    finally:
        if client_sock is not None:
            try:
                client_sock.close()
            except OSError:
                pass
        try:
            server_sock.close()
        except OSError:
            pass
        _cleanup_socket(sock_path)
        _cleanup_pid_file(args.pid_file)

    return 0


if __name__ == "__main__":
    sys.exit(main())
