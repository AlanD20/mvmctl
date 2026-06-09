"""Cross-key constraint validators for overridable settings.

Constraints receive (key_being_set, resolve_fn) and raise ConfigError
if the pending change would create an invalid state.

resolve_fn(key, category=None) returns the effective value:
  - new_value if the key matches and category matches that being set
  - current DB/default for any other key/category
"""

from __future__ import annotations

import re
from collections.abc import Callable
from typing import Any

from mvmctl.exceptions import ConfigError

# A resolve callable: resolve(key, category=None) -> effective value
ResolveFn = Callable[..., Any]

# A constraint receives (key_being_set, resolve_fn) and raises on invalid state
Constraint = Callable[[str, ResolveFn], None]


class ConstraintRegistry:
    """Registry of cross-key validation constraints for overridable settings."""

    def __init__(self) -> None:
        self._constraints: dict[tuple[str, str], list[Constraint]] = {}

    def register(
        self, category: str, keys: frozenset[str], constraint: Constraint
    ) -> None:
        """Register a constraint that fires when any of `keys` in `category` is set."""
        for key in keys:
            self._constraints.setdefault((category, key), []).append(constraint)

    def get(self, category: str, key: str) -> list[Constraint]:
        """Return constraints for a (category, key) pair."""
        return self._constraints.get((category, key), [])


# Singleton instance — constraint definitions live here
constraints = ConstraintRegistry()


# --- Built-in constraints ---


def _validate_nocloud_port_range(_key: str, resolve: ResolveFn) -> None:
    """Ensure nocloud_port_range_end > nocloud_port_range_start."""
    start = int(resolve("nocloud_port_range_start"))
    end = int(resolve("nocloud_port_range_end"))
    if end <= start:
        raise ConfigError(
            f"nocloud_port_range_end ({end}) must be greater than "
            f"nocloud_port_range_start ({start})"
        )


constraints.register(
    "defaults.cloudinit",
    frozenset({"nocloud_port_range_start", "nocloud_port_range_end"}),
    _validate_nocloud_port_range,
)

_MAC_PREFIX_RE = re.compile(r"^[0-9a-fA-F]{2}:[0-9a-fA-F]{2}$")


def _validate_mac_prefix(_key: str, resolve: ResolveFn) -> None:
    """Ensure guest_mac_prefix is a valid 2-byte hex MAC prefix (e.g. '02:FC')."""
    prefix = str(resolve("guest_mac_prefix"))
    if not _MAC_PREFIX_RE.match(prefix):
        raise ConfigError(
            f"Invalid MAC prefix '{prefix}'. "
            f"Must be two hex bytes separated by a colon (e.g. '02:FC')."
        )


constraints.register(
    "defaults.vm",
    frozenset({"guest_mac_prefix"}),
    _validate_mac_prefix,
)
