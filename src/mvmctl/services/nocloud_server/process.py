"""
NoCloud-net standalone HTTP server process.

This module runs as a standalone subprocess to serve cloud-init files
(meta-data, user-data, network-config) to VMs via the nocloud-net
datasource mechanism.

It is designed to run as a persistent process that survives beyond
the CLI process lifetime.
"""

import argparse
import os
import signal
import sys
from http.server import HTTPServer, SimpleHTTPRequestHandler
from pathlib import Path


class _CloudInitRequestHandler(SimpleHTTPRequestHandler):
    """
    Custom request handler for cloud-init files.

    Serves files from the specified cloud-init directory with
    proper content types for cloud-init consumption.
    """

    def log_message(self, format: str, *args: str) -> None:
        """Suppress HTTP request logging to stderr."""
        pass

    def end_headers(self) -> None:
        """Add headers to prevent caching."""
        self.send_header("Cache-Control", "no-store, no-cache, must-revalidate")
        self.send_header("Pragma", "no-cache")
        super().end_headers()


def _signal_handler(signum: int, frame: object) -> None:
    """Handle SIGTERM gracefully to allow clean shutdown."""
    print(f"NoCloud-net server received signal {signum}, shutting down...")
    # The server will be shut down in the main loop
    global _shutdown_requested  # noqa: F841
    _shutdown_requested = True


_shutdown_requested = False


def main() -> None:
    """Main entry point for the standalone server process."""
    global _shutdown_requested  # noqa: F841

    parser = argparse.ArgumentParser(
        description="NoCloud-net HTTP server for cloud-init datasource"
    )
    parser.add_argument(
        "--cloud-init-dir",
        required=True,
        type=Path,
        help="Directory containing cloud-init files",
    )
    parser.add_argument(
        "--port",
        required=True,
        type=int,
        help="Port to listen on",
    )
    parser.add_argument(
        "--host",
        required=True,
        help="Host address to bind to",
    )
    parser.add_argument(
        "--pid-file",
        required=True,
        type=Path,
        help="Path to write PID file",
    )
    parser.add_argument(
        "--log-file",
        required=True,
        type=Path,
        help="Path to write log file",
    )

    args = parser.parse_args()

    # Validate cloud-init directory
    if not args.cloud_init_dir.exists():
        print(
            f"Error: Cloud-init directory does not exist: {args.cloud_init_dir}",
            file=sys.stderr,
        )
        sys.exit(1)
    if not args.cloud_init_dir.is_dir():
        print(
            f"Error: Cloud-init path is not a directory: {args.cloud_init_dir}",
            file=sys.stderr,
        )
        sys.exit(1)

    # Write PID file
    try:
        args.pid_file.parent.mkdir(parents=True, exist_ok=True)
        args.pid_file.write_text(str(os.getpid()))
    except OSError as e:
        print(f"Error: Cannot write PID file: {e}", file=sys.stderr)
        sys.exit(1)

    try:
        log_fp = open(args.log_file, "w", buffering=1, encoding="utf-8")
        sys.stdout = log_fp
        sys.stderr = log_fp
    except OSError as e:
        print(f"Error: Cannot open log file: {e}", file=sys.stderr)
        sys.exit(1)

    # Set up signal handler for graceful shutdown
    signal.signal(signal.SIGTERM, _signal_handler)
    signal.signal(signal.SIGINT, _signal_handler)

    # Create handler class bound to our directory
    cloud_init_dir = args.cloud_init_dir

    class _BoundHandler(_CloudInitRequestHandler):
        """Handler bound to specific cloud-init directory."""

        def translate_path(self, path: str) -> str:
            """Translate URL path to filesystem path."""
            import posixpath
            import urllib.parse

            path = urllib.parse.unquote(path)
            path = posixpath.normpath(path)
            words = path.split("/")
            words = [w for w in words if w]
            result_path = str(cloud_init_dir)
            for word in words:
                if os.path.dirname(word) or word in (os.curdir, os.pardir):
                    continue
                result_path = os.path.join(result_path, word)
            return result_path

    try:
        server = HTTPServer((args.host, args.port), _BoundHandler)
        print(f"NoCloud-net HTTP server starting on {args.host}:{args.port}")
        print(f"Serving cloud-init files from: {args.cloud_init_dir}")
        print(f"PID written to: {args.pid_file}")

        # Main server loop with graceful shutdown support
        while not _shutdown_requested:
            server.timeout = 1  # Check for shutdown every second (int seconds)
            try:
                server.handle_request()
            except Exception:
                if not _shutdown_requested:
                    raise

        # Graceful shutdown
        server.shutdown()
        print("NoCloud-net HTTP server stopped")
    except OSError as e:
        print(f"Error starting server: {e}", file=sys.stderr)
        sys.exit(1)
    finally:
        try:
            if args.pid_file.exists():
                args.pid_file.unlink()
        except OSError:
            pass
        try:
            log_fp.close()
        except Exception:
            pass


if __name__ == "__main__":
    main()
