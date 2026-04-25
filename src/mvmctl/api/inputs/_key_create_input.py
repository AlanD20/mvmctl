"""SSH key creation input models for API boundary."""

from __future__ import annotations

import socket
from dataclasses import dataclass
from pathlib import Path

from mvmctl.exceptions import MVMKeyError
from mvmctl.utils.common import CacheUtils

__all__ = [
    "KeyCreateInput",
    "KeyCreateRequest",
    "ResolvedKeyCreateInput",
]


@dataclass
class KeyCreateInput:
    """Input model for SSH key creation."""

    name: str
    algorithm: str | None = None  # "ed25519", "rsa", "ecdsa"
    bits: int | None = None
    output_dir: Path | None = None
    comment: str | None = None
    overwrite: bool = False
    set_default: bool = False


@dataclass(frozen=True)
class ResolvedKeyCreateInput:
    """Immutable resolved inputs for key creation."""

    name: str
    algorithm: str
    bits: int | None
    output_dir: Path
    comment: str
    overwrite: bool
    set_default: bool


class KeyCreateRequest:
    """Resolve and validate key creation inputs."""

    _result: ResolvedKeyCreateInput | None = None

    def __init__(self, *, inputs: KeyCreateInput) -> None:
        self._inputs = inputs

    @property
    def result(self) -> ResolvedKeyCreateInput | None:
        return self._result

    def resolve(self) -> ResolvedKeyCreateInput:
        """Resolve defaults and validate."""
        # Default algorithm
        algorithm = self._inputs.algorithm or "ed25519"

        # Validate algorithm
        valid = ("ed25519", "rsa", "ecdsa")
        if algorithm not in valid:
            raise MVMKeyError(
                f"Invalid algorithm: '{algorithm}'. "
                f"Valid choices: {', '.join(valid)}"
            )

        # Default comment
        comment = self._inputs.comment or (
            f"{self._inputs.name}@{socket.gethostname()}"
        )

        # Default output_dir resolved via CacheUtils
        output_dir = self._inputs.output_dir or CacheUtils.get_keys_dir()

        self._result = ResolvedKeyCreateInput(
            name=self._inputs.name,
            algorithm=algorithm,
            bits=self._inputs.bits,
            output_dir=output_dir,
            comment=comment,
            overwrite=self._inputs.overwrite,
            set_default=self._inputs.set_default,
        )
        return self._result
