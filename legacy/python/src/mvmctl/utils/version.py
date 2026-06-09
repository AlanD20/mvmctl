"""Binary version gating for feature availability."""

from __future__ import annotations

import logging
import re

from mvmctl.exceptions import VersionGateError

logger = logging.getLogger(__name__)


class VersionGate:
    """Gate features behind a minimum binary version.

    Usage:
        VersionGate.require("firecracker", "1.15.1", "1.16")
        # Raises VersionGateError if "1.15.1" < "1.16"
    """

    @staticmethod
    def require(binary_name: str, version: str | None, minimum: str) -> None:
        """Require *version* to meet the *minimum* requirement.

        Args:
            binary_name: Human-readable binary name (e.g. "Firecracker").
            version: The version string to check (e.g. "1.15.1", "dev-abc123").
            minimum: Minimum version requirement (e.g. "1.16", "2", "1.16.0").

        Raises:
            VersionGateError: If *version* is too old or cannot be parsed.
        """
        if version is None:
            raise VersionGateError(
                f"Cannot determine {binary_name} version. "
                f"{binary_name} v{minimum}+ required."
            )

        # Dev builds always pass the gate.
        if version.startswith("dev-"):
            return

        if not VersionGate._is_satisfied_by(version, minimum):
            raise VersionGateError(
                f"{binary_name} v{minimum}+ required for this operation "
                f"(current: v{version}). "
                f"Stop the VM first, perform the operation, then start it again."
            )

    @staticmethod
    def _parse_version(version: str) -> tuple[int, ...]:
        """Parse a semver-like version string into a tuple of ints.

        Handles formats like "1", "1.15", "1.15.1".
        Non-numeric suffixes (e.g., "1.15.1-dev") are stripped.
        Raises ValueError if no numeric components found.
        """
        # Strip non-numeric suffixes after the third component
        clean = re.sub(r"^(\d+(?:\.\d+)*).*$", r"\1", version)
        parts = clean.split(".")
        return tuple(int(p) for p in parts)

    @staticmethod
    def _is_satisfied_by(version: str, minimum: str) -> bool:
        """Compare version against minimum.

        Examples:
            (1, 15, 1) >= (1, 16)    -> False  (15 < 16)
            (1, 16, 0) >= (1, 16)    -> True
            (2, 0) >= (1, 16)        -> True  (2 > 1)
            (1, 16) >= (1, 16, 0)    -> True  (only compare first 2)
        """
        try:
            v_parts = VersionGate._parse_version(version)
            m_parts = VersionGate._parse_version(minimum)
        except (ValueError, IndexError):
            logger.warning(
                "Could not parse version '%s' against minimum '%s'",
                version,
                minimum,
            )
            return False

        # Compare component-by-component, stopping at the shorter tuple
        for v, m in zip(v_parts, m_parts):
            if v > m:
                return True
            if v < m:
                return False

        # All compared components are equal — satisfied if version has at least
        # as many components as minimum (or more).
        return len(v_parts) >= len(m_parts)
