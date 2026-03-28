"""Cloud-init status detection via console log polling."""

import logging
import re
import time
from pathlib import Path

from mvmctl.constants import (
    CONST_CLOUD_INIT_POLL_INTERVAL_S,
    CONST_CLOUD_INIT_TIMEOUT_S,
    DEFAULT_CLOUD_INIT_FINAL_MESSAGE,
)
from mvmctl.models.vm import CloudInitStatus

logger = logging.getLogger(__name__)

# Regex pattern for detecting cloud-init completion marker
_CLOUD_INIT_DONE_PATTERN = re.compile(
    rf"final_message.*['\"]({re.escape(DEFAULT_CLOUD_INIT_FINAL_MESSAGE)})['\"]"
)


def check_cloud_init_status(vm_name: str, console_log_path: Path) -> CloudInitStatus:
    """Check cloud-init status by polling the console log file.

    Args:
        vm_name: VM name (used for logging).
        console_log_path: Path to the VM's console log file.

    Returns:
        CloudInitStatus enum value:
        - PENDING: Console log file doesn't exist yet
        - RUNNING: File exists but no "done" marker found
        - DONE: Final message marker detected
    """
    if not console_log_path.exists():
        logger.debug("Console log not found for VM '%s': %s", vm_name, console_log_path)
        return CloudInitStatus.PENDING

    try:
        content = console_log_path.read_text(encoding="utf-8")
        if _CLOUD_INIT_DONE_PATTERN.search(content):
            logger.info("Cloud-init completed for VM '%s'", vm_name)
            return CloudInitStatus.DONE
        else:
            logger.debug("Cloud-init still running for VM '%s'", vm_name)
            return CloudInitStatus.RUNNING
    except IOError as e:
        logger.warning("Error reading console log for VM '%s': %s", vm_name, e)
        return CloudInitStatus.RUNNING


def wait_for_cloud_init_done(
    vm_name: str,
    console_log_path: Path,
    timeout: int | None = None,
) -> bool:
    """Wait for cloud-init to complete by polling the console log.

    Args:
        vm_name: VM name (used for logging).
        console_log_path: Path to the VM's console log file.
        timeout: Maximum seconds to wait (default: CONST_CLOUD_INIT_TIMEOUT_S).

    Returns:
        True if cloud-init "done" marker was found, False on timeout.
    """
    if timeout is None:
        timeout = CONST_CLOUD_INIT_TIMEOUT_S

    logger.info(
        "Waiting for cloud-init completion for VM '%s' (timeout=%ds)",
        vm_name,
        timeout,
    )

    deadline = time.time() + timeout
    poll_interval = CONST_CLOUD_INIT_POLL_INTERVAL_S

    while time.time() < deadline:
        status = check_cloud_init_status(vm_name, console_log_path)

        if status == CloudInitStatus.DONE:
            return True

        if status == CloudInitStatus.PENDING:
            # Log at debug level to avoid spam; only info on first check
            remaining = deadline - time.time()
            logger.debug(
                "Cloud-init not started yet for VM '%s' (%.0fs remaining)",
                vm_name,
                remaining,
            )

        # Sleep before next poll
        sleep_time = min(poll_interval, deadline - time.time())
        if sleep_time > 0:
            time.sleep(sleep_time)

    logger.warning(
        "Cloud-init timeout (%ds) reached for VM '%s'",
        timeout,
        vm_name,
    )
    return False
