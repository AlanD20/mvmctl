from __future__ import annotations

import logging
import os
import signal
import time

logger = logging.getLogger(__name__)


class ProcessSignalHandler:
    """Handle process signals and lifecycle operations."""

    def __init__(self, pid: int):
        self.pid = pid

    def is_running(self) -> bool:
        """Check if process is still running."""
        try:
            os.kill(self.pid, 0)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    def send_signal(self, sig: int) -> bool:
        """Send signal to process. Returns True if signal sent, False if process already dead."""
        try:
            os.kill(self.pid, sig)
            return True
        except (ProcessLookupError, PermissionError):
            return False

    def graceful_shutdown(self, timeout: int = 10, sigterm_wait: float = 2.0) -> bool:
        """Send SIGTERM, wait, then SIGKILL if needed. Returns True if shutdown successful."""
        if not self.is_running():
            return True

        # Try SIGTERM first
        self.send_signal(signal.SIGTERM)
        time.sleep(sigterm_wait)

        if not self.is_running():
            return True

        # Process still running, escalate to SIGKILL
        self.send_signal(signal.SIGKILL)

        # Wait up to timeout for process to die
        poll_interval = 0.1
        elapsed = sigterm_wait
        while elapsed < timeout:
            if not self.is_running():
                return True
            time.sleep(poll_interval)
            elapsed += poll_interval

        return not self.is_running()
