"""Image input models for API boundary."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.image._resolver import ImageResolver
from mvmctl.exceptions import ImageAcquireError
from mvmctl.utils.common import CacheUtils

__all__ = [
    "ImageImportInput",
    "ImageFetchInput",
    "ImageAcquireRequest",
    "ResolvedImageAcquireInput",
]

CLI_TO_INTERNAL_DETECTOR = {
    "type": "type_code",
    "label": "label",
    "size": "size",
    "filesystem": "filesystem",
}

FIRECRACKER_SUPPORTED_ARCH = ["x86_64", "amd64", "aarch64", "arm64"]


@dataclass
class ImageImportInput:
    """Specification for importing a local image file."""

    name: str
    format: str  # noqa: N816
    source_path: Path
    force: bool = False
    arch: str | None = None
    set_default: bool = False
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


@dataclass
class ImageFetchInput:
    """Input model for image fetch and registration operations."""

    os_slug: str
    type: str
    force: bool = False
    set_default: bool = False
    arch: str | None = None
    version: str | None = None
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


@dataclass
class ResolvedImageAcquireInput:
    """Resolved input model for image fetch and registration operations."""

    os_slug: str
    type: str
    output_dir: Path
    force: bool = False
    arch: str | None = None
    format: str | None = None
    set_default: bool = False
    version: str | None = None
    partition: int | None = None
    source_path: Path | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


class ImageAcquireRequest:
    _result: ResolvedImageAcquireInput | None = None

    def __init__(
        self,
        *,
        inputs: ImageFetchInput | ImageImportInput,
        db: Database | None = None,
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._image_resolver = ImageResolver(ImageRepository(self._db))

    @property
    def result(self) -> ResolvedImageAcquireInput | None:
        return self._result

    def resolve_fetch(self) -> ResolvedImageAcquireInput:
        if not isinstance(self._inputs, ImageFetchInput):
            raise ImageAcquireError("Expected ImageFetchInput")

        # Default arch
        if self._inputs.arch is not None:
            arch = self._inputs.arch
        else:
            arch = SettingsService.resolve(self._db, "defaults.image", "arch")

        # Resolve disabled detectors
        disabled = self._resolve_disabled_detectors(
            self._inputs.disabled_detectors
        )

        self._result = ResolvedImageAcquireInput(
            os_slug=self._inputs.os_slug,
            type=self._inputs.type,
            force=self._inputs.force,
            set_default=self._inputs.set_default,
            arch=arch,
            version=self._inputs.version,
            partition=self._inputs.partition,
            output_dir=CacheUtils.get_images_dir(),
            skip_optimization=self._inputs.skip_optimization,
            disabled_detectors=disabled,
        )

        self.ensure_validate()
        return self._result

    def resolve_import(self) -> ResolvedImageAcquireInput:
        if not isinstance(self._inputs, ImageImportInput):
            raise ImageAcquireError("Expected ImageImportInput")

        # Default arch
        if self._inputs.arch is not None:
            arch = self._inputs.arch
        else:
            arch = SettingsService.resolve(self._db, "defaults.image", "arch")

        # Resolve disabled detectors
        disabled = self._resolve_disabled_detectors(
            self._inputs.disabled_detectors
        )

        self._result = ResolvedImageAcquireInput(
            os_slug=self._inputs.name,
            arch=arch,
            type="custom",
            source_path=self._inputs.source_path,
            format=self._inputs.format,
            output_dir=CacheUtils.get_images_dir(),
            disabled_detectors=disabled,
            force=self._inputs.force,
            partition=self._inputs.partition,
            set_default=self._inputs.set_default,
            skip_optimization=self._inputs.skip_optimization,
        )

        self.ensure_validate()
        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved dependencies."""

        if self._result is None:
            raise ImageAcquireError(
                "Failed to resolve necessary dependencies to validate"
            )

        arch: str | None = None
        partition: int | None = None

        if isinstance(self._result, ResolvedImageAcquireInput):
            arch = self._result.arch
            partition = self._result.partition

        if arch is not None and arch not in FIRECRACKER_SUPPORTED_ARCH:
            raise ImageAcquireError(
                f"Unknown arch: {arch}. Valid: {', '.join(FIRECRACKER_SUPPORTED_ARCH)}"
            )

        if partition is not None and partition < 1:
            raise ImageAcquireError("Partition cannot be less than 1")

    def ensure_validate_import(self) -> None:

        if self._result is None:
            raise ImageAcquireError(
                "Failed to resolve necessary dependencies to validate"
            )

    def _resolve_disabled_detectors(self, detectors: list[str]) -> list[str]:
        disabled: list[str] = []
        for name in detectors:
            if name == "all":
                return list(CLI_TO_INTERNAL_DETECTOR.values())
            if name in CLI_TO_INTERNAL_DETECTOR:
                disabled.append(CLI_TO_INTERNAL_DETECTOR[name])
            else:
                raise ImageAcquireError(
                    f"Unknown detector: {name}. Valid: type,label,size,filesystem,all"
                )
        return disabled
