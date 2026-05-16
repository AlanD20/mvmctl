"""Shared version resolution utilities — pure data parsing, no I/O.

Provides ``VersionResolver``, a pure utility class with no database, network,
or filesystem dependencies. It handles semver parsing, version spec resolution,
and selector (``type:version``) splitting — consolidating ad-hoc version
parsing that was scattered across domains.
"""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.exceptions import VersionError

__all__ = [
    "VersionError",
    "VersionResolver",
    "VersionSpec",
]


@dataclass
class VersionSpec:
    """Specification for a version to resolve.

    Created by :meth:`VersionResolver.parse_spec` and consumed by
    :meth:`VersionResolver.resolve`.

    Attributes:
        major: Major version number, or None for partial specs.
        minor: Minor version number, or None for partial specs.
        patch: Patch version number, or None for partial specs.
        is_latest: True when ``latest`` was requested explicitly.
    """

    major: int | None = None
    minor: int | None = None
    patch: int | None = None
    is_latest: bool = False

    @property
    def is_partial(self) -> bool:
        """Return True when any of major/minor/patch is None."""
        return self.major is None or self.minor is None or self.patch is None


class VersionResolver:
    """Pure utility for parsing version specs and resolving against version lists.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def parse_spec(spec: str) -> VersionSpec:
        """Parse a version specification string into a structured ``VersionSpec``.

        Rules:

        * ``""`` or ``"latest"`` → ``VersionSpec(is_latest=True)``
        * ``"1"`` → ``VersionSpec(major=1)``
        * ``"1.15"`` → ``VersionSpec(major=1, minor=15)``
        * ``"1.15.1"`` → ``VersionSpec(major=1, minor=15, patch=1)``
        * ``"v1.15.1"`` → strips ``v`` prefix first

        Args:
            spec: The version specification string.

        Returns:
            A :class:`VersionSpec` instance.

        """
        spec = spec.strip()

        if not spec or spec == "latest":
            return VersionSpec(is_latest=True)

        # Strip 'v' prefix
        if spec.startswith("v") or spec.startswith("V"):
            spec = spec[1:]

        parts = spec.split(".")
        major: int | None = int(parts[0]) if len(parts) >= 1 else None
        minor: int | None = int(parts[1]) if len(parts) >= 2 else None
        patch: int | None = int(parts[2]) if len(parts) >= 3 else None

        return VersionSpec(major=major, minor=minor, patch=patch)

    @staticmethod
    def parse_selector(selector: str) -> tuple[str | None, str]:
        """Split a ``type:version`` selector into its two parts.

        Splits on ``:`` with *maxsplit=1*.

        * ``"firecracker:1.15"`` → ``("firecracker", "1.15")``
        * ``"1.15"`` → ``(None, "1.15")``
        * ``"firecracker"`` → ``("firecracker", "")``
        * ``":1.15"`` → ``(None, "1.15")``
        * ``"firecracker:"`` → ``("firecracker", "")``

        Args:
            selector: The selector string, optionally containing ``:``.

        Returns:
            A ``(prefix, value)`` tuple. *prefix* is ``None`` when no
            ``:`` was found or the part before ``:`` is empty.

        """
        if ":" not in selector:
            return (None, selector)

        prefix, value = selector.split(":", maxsplit=1)
        if not prefix:
            return (None, value)
        return (prefix, value)

    @staticmethod
    def resolve(versions: list[str], spec: VersionSpec) -> str:
        """Resolve a ``VersionSpec`` against a list of available versions.

        1. Sorts versions descending by semver (newest first).
        2. If ``spec.is_latest`` → returns highest version.
        3. If exact version (all parts set) → verifies existence, returns it.
        4. If partial → iterates sorted versions, finds first that matches
           the given prefix parts.

        Args:
            versions: List of version strings (e.g. ``["1.15.0", "1.14.0"]``).
            spec: The version specification to resolve.

        Returns:
            The matching version string.

        Raises:
            VersionError: If no matching version is found.

        """
        if not versions:
            raise VersionError(f"No versions available to resolve spec {spec}")

        # Work on a copy — never mutate the input list
        sorted_versions = sorted(
            versions, key=VersionResolver.semver_key, reverse=True
        )

        if spec.is_latest:
            return sorted_versions[0]

        # Count how many parts are set in the spec
        spec_parts: list[int] = []
        if spec.major is not None:
            spec_parts.append(spec.major)
        if spec.minor is not None:
            spec_parts.append(spec.minor)
        if spec.patch is not None:
            spec_parts.append(spec.patch)

        if (
            spec.major is not None
            and spec.minor is not None
            and spec.patch is not None
        ):
            # Exact version — check if it exists in the list
            target = ".".join(str(p) for p in spec_parts)
            for v in versions:
                if v == target or v.removeprefix("v") == target:
                    return target
            raise VersionError(
                f"Version '{target}' not found in available versions: {versions}"
            )

        # Partial match — iterate sorted versions, find first matching prefix
        n = len(spec_parts)
        for v in sorted_versions:
            v_clean = v.removeprefix("v")
            v_parts = v_clean.split(".")
            if len(v_parts) >= n:
                v_prefix = tuple(int(x) for x in v_parts[:n])
                if v_prefix == tuple(spec_parts[:n]):
                    return v_clean

        raise VersionError(
            f"No version matching spec (major={spec.major}, "
            f"minor={spec.minor}, patch={spec.patch}) "
            f"found in available versions: {versions}"
        )

    @staticmethod
    def semver_key(v: str) -> tuple[int, ...]:
        """Convert a semver string to a sortable tuple of integers.

        Strips ``v`` prefix, splits on ``.``, converts each part to ``int``.
        On parse failure, returns ``(0,)`` so failed versions sort to the end.

        Args:
            v: Version string like ``"1.15.0"``.

        Returns:
            Tuple of integers for descending sort.

        """
        clean = v.removeprefix("v")
        try:
            return tuple(int(x) for x in clean.split("."))
        except ValueError:
            return (0,)
