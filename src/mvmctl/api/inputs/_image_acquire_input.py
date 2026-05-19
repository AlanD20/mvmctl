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
    "ImagePullInput",
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
    source_path: Path
    force: bool = False
    format: str | None = None
    arch: str | None = None
    set_default: bool = False
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


@dataclass
class ImagePullInput:
    """Input model for image pull and registration operations."""

    type: str
    name: str | None = None
    force: bool = False
    set_default: bool = False
    arch: str | None = None
    version: str | None = None
    no_cache: bool = False
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


@dataclass
class ResolvedImageAcquireInput:
    """Resolved input model for image pull and registration operations."""

    type: str
    arch: str
    output_dir: Path
    name: str | None = None
    source_path: Path | None = None
    version: str | None = None
    no_cache: bool = False
    force: bool = False
    format: str | None = None
    set_default: bool = False
    partition: int | None = None
    skip_optimization: bool = False
    disabled_detectors: list[str] = field(default_factory=list)


class ImageAcquireRequest:
    _result: ResolvedImageAcquireInput | None = None

    def __init__(
        self,
        *,
        inputs: ImagePullInput | ImageImportInput,
        db: Database | None = None,
    ) -> None:
        """Initialize the resolver with database and sub-resolvers."""
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._image_resolver = ImageResolver(ImageRepository(self._db))

    @property
    def result(self) -> ResolvedImageAcquireInput | None:
        return self._result

    def resolve_pull(self) -> ResolvedImageAcquireInput:
        if not isinstance(self._inputs, ImagePullInput):
            raise ImageAcquireError("Expected ImagePullInput")

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
            type=self._inputs.type,
            name=self._inputs.name,
            force=self._inputs.force,
            set_default=self._inputs.set_default,
            arch=arch,
            version=self._inputs.version,
            no_cache=self._inputs.no_cache,
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

        # Default format
        if self._inputs.format is not None:
            format_val = self._inputs.format
        else:
            format_val = str(
                SettingsService.resolve(
                    self._db, "defaults.image", "import_format"
                )
            )

        self._result = ResolvedImageAcquireInput(
            type=self._inputs.name,
            name=self._inputs.name,
            arch=arch,
            source_path=self._inputs.source_path,
            format=format_val,
            output_dir=CacheUtils.get_images_dir(),
            disabled_detectors=disabled,
            force=self._inputs.force,
            partition=self._inputs.partition,
            set_default=self._inputs.set_default,
            skip_optimization=self._inputs.skip_optimization,
        )

        self.ensure_validate()
        self.ensure_validate_import()
        return self._result

    def ensure_validate(self) -> None:
        """Validate resolved dependencies."""

        if self._result is None:
            raise ImageAcquireError(
                "Failed to resolve necessary dependencies to validate"
            )

        arch: str = ""
        partition: int | None = None

        if isinstance(self._result, ResolvedImageAcquireInput):
            arch = self._result.arch
            partition = self._result.partition

        if arch not in FIRECRACKER_SUPPORTED_ARCH:
            raise ImageAcquireError(
                f"Unknown arch: {arch}. Valid: {', '.join(FIRECRACKER_SUPPORTED_ARCH)}"
            )

        if partition is not None and partition < 1:
            raise ImageAcquireError("Partition cannot be less than 1")

    def ensure_validate_import(self) -> None:
        """Validate import-specific fields."""
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
