"""NoCloud server service defaults.

These constants are specific to the NoCloud server service and are used
by both the manager (manager.py) and the standalone process (process.py).
"""

# Process management
DEFAULT_NOCLOUD_PID_FILENAME: str = "nocloud-server.pid"
DEFAULT_NOCLOUD_LOG_FILENAME: str = "cloud-init.log"

# Port allocation
CONST_NO_CLOUD_NET_PORT_RANGE: tuple[int, int] = (8000, 9000)
CONST_NO_CLOUD_NET_MAX_PORT_RETRIES: int = 1000
CONST_NO_CLOUD_NET_BIND_TIMEOUT_S: float = 0.5

__all__ = [
    "DEFAULT_NOCLOUD_PID_FILENAME",
    "DEFAULT_NOCLOUD_LOG_FILENAME",
    "CONST_NO_CLOUD_NET_PORT_RANGE",
    "CONST_NO_CLOUD_NET_MAX_PORT_RETRIES",
    "CONST_NO_CLOUD_NET_BIND_TIMEOUT_S",
]
