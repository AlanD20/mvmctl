"""Kernel service — stateless kernel operations (download, build, configure)."""

from __future__ import annotations

import functools
import hashlib
import logging
import re
import shutil
import subprocess
import tarfile
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from mvmctl.constants import (
    CONST_FILE_PERMS_EXECUTABLE,
    CONST_MEBIBYTE_BYTES,
    HTTP_TIMEOUT_KERNEL_CONFIG_S,
    HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
    HTTP_TIMEOUT_SHA256_FETCH_S,
    HTTP_TIMEOUT_SHA256_SIDECAR_S,
    KERNEL_TYPE_FIRECRACKER,
    KERNEL_TYPE_OFFICIAL,
)
from mvmctl.core._shared import AssetManager
from mvmctl.core._shared._http_dir_version_resolver import (
    HttpDirVersionResolver,
    VersionInfo,
)
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.exceptions import (
    ChecksumMismatchError,
    HttpDownloadError,
    KernelError,
    MVMError,
    ProcessError,
)
from mvmctl.models import KernelItem, KernelPullResult, KernelSpec
from mvmctl.utils._system import run_cmd
from mvmctl.utils.common import CacheUtils
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_optional_template, render_template
from mvmctl.utils.yaml import (
    optional_int,
    optional_str,
    parse_set_val_list,
    require_str,
    require_str_list,
)

logger = logging.getLogger(__name__)
_BUILD_LOG_PATTERNS = re.compile(
    r"(?i)(warning|error|cannot find|undefined reference|fatal|note:)",
)


@dataclass
class KernelConfigResult:
    """Result from kernel configuration step."""

    success: bool
    warnings: list[str]
    info_messages: list[str]


@dataclass
class KernelBuildResult:
    """Result from kernel build step."""

    success: bool
    warnings: list[str]
    info_messages: list[str]


@dataclass
class KernelPipelineResult:
    """Result from the complete kernel build pipeline."""

    config_result: KernelConfigResult | None
    build_result: KernelBuildResult | None
    success: bool


@dataclass
class ParsedKernelFilename:
    """Parsed components from a kernel filename."""

    base_name: str
    version: str
    arch: str


class KernelService:
    """
    Stateless kernel service — handles downloading and building kernels.
    Args:
        repo: KernelRepository for DB operations. Must be provided.
    """

    def __init__(self, repo: KernelRepository) -> None:
        self._repo = repo

    def list_all(self, verify: bool = True) -> list[KernelItem]:
        """
        List all kernels, syncing is_present flag with filesystem.
        Args:
            verify: If True (default), check filesystem and update DB.
                   If False, return DB records as-is.
        """
        kernels = self._repo.list_all()
        if not verify:
            return kernels
        missing_ids: list[str] = []
        for kernel in kernels:
            if not kernel.resolved_path.exists():
                missing_ids.append(kernel.id)
        if missing_ids:
            self._repo.update_many_is_present(missing_ids, False)
            kernels = self._repo.list_all()
        return kernels

    def remove(self, kernel: KernelItem, *, force: bool = False) -> KernelItem:
        """
        Remove a single kernel from disk and database.

        Hard-deletes when no VMs reference the kernel.
        Soft-deletes only when VMs still reference it (to preserve history).

        Args:
            kernel: The KernelItem to remove.
            force: If True, remove even if referenced by VMs.

        Returns:
            The removed KernelItem.

        Raises:
            KernelError: If kernel is referenced by VMs and force is False.

        """
        vms = kernel.vms or []
        has_vms = bool(vms)

        if has_vms and not force:
            raise KernelError(
                f"Kernel referenced by VMs: {', '.join(v.name for v in vms)}"
            )

        # Delete file from disk
        kernel_path = kernel.resolved_path
        if kernel_path.exists():
            kernel_path.unlink()

        # Hard delete if no VMs, soft delete if VMs exist (with force)
        if has_vms:
            self._repo.soft_delete(kernel.id)
        else:
            self._repo.delete(kernel.id)

        return kernel

    def remove_many(
        self, kernels: list[KernelItem], *, force: bool = False
    ) -> list[KernelItem]:
        """
        Remove multiple kernels.
        Args:
            kernels: List of KernelItem to remove.
            force: If True, remove even if referenced by VMs.
        Returns:
            The removed KernelItem list.
        """
        deleted: list[KernelItem] = []
        for kernel in kernels:
            self.remove(kernel, force=force)
            deleted.append(kernel)
        return deleted

    @staticmethod
    @functools.lru_cache(maxsize=1)
    def _load_specs() -> dict[str, KernelSpec]:
        """Load and parse all kernel specs from kernels.yaml (cached)."""
        import yaml

        try:
            kernels_yaml = AssetManager().read_file("kernels.yaml")
            data: Any = yaml.safe_load(kernels_yaml) or {}
        except Exception as exc:
            if "yaml" in str(type(exc)).lower():
                raise KernelError(
                    f"Failed to load kernels.yaml: {exc}"
                ) from exc
            raise KernelError(f"Failed to load kernels.yaml: {exc}") from exc
        if not isinstance(data, dict):
            raise KernelError("Invalid kernels.yaml: expected mapping at root")
        specs: dict[str, KernelSpec] = {}
        for spec_name, raw_any in data.items():
            if not isinstance(spec_name, str) or not isinstance(raw_any, dict):
                raise KernelError("Invalid kernels.yaml entry format")
            raw: dict[str, Any] = raw_any
            try:
                opts_raw = raw.get("options")
                opts: dict[str, Any] = (
                    opts_raw if isinstance(opts_raw, dict) else {}
                )
                specs[spec_name] = KernelSpec(
                    name=spec_name,
                    kernel_type=require_str(raw, "type"),
                    version=require_str(raw, "version"),
                    source=require_str(raw, "source"),
                    output_name=require_str(raw, "output_name"),
                    build_dir=require_str(raw, "build_dir"),
                    list_url_template=optional_str(raw, "list_url_template"),
                    config_url_template=optional_str(
                        raw, "config_url_template"
                    ),
                    sha256=optional_str(raw, "sha256"),
                    sha256_url=optional_str(raw, "sha256_url"),
                    parallel_jobs=optional_int(raw, "parallel_jobs"),
                    config_fragments=require_str_list(raw, "config_fragments"),
                    enabled_configs=require_str_list(raw, "enabled_configs"),
                    disabled_configs=require_str_list(raw, "disabled_configs"),
                    required_settings=require_str_list(
                        raw, "required_settings"
                    ),
                    set_val_configs=parse_set_val_list(raw, "set_val_configs"),
                    resolver=optional_str(raw, "resolver"),
                    versions_url=optional_str(raw, "versions_url"),
                    options=opts if opts else None,
                    file_pattern=optional_str(opts, "file_pattern"),
                    file_suffix=optional_str(opts, "file_suffix"),
                )
            except ValueError as exc:
                raise KernelError(
                    f"Invalid kernels.yaml entry '{spec_name}': {exc}"
                ) from exc
        return specs

    @classmethod
    def get_specs_for(
        cls,
        names: list[str] | None = None,
        kernel_type: str | None = None,
        version: str | None = None,
    ) -> list[KernelSpec]:
        """
        Return kernel specs filtered by criteria.
        Args:
            names: Filter by spec name(s) (YAML keys like 'firecracker-5.10').
            kernel_type: Filter by kernel type ('firecracker' or 'official').
            version: Filter by version string.
        Returns:
            List of matching KernelSpec objects.
        Raises:
            KernelError: If any requested name is not found in the catalog.
        """
        all_specs = cls._load_specs()
        # Fast path: names-only lookup with O(1) dict access
        if names is not None and kernel_type is None and version is None:
            name_set = set(names)
            results = [s for n, s in all_specs.items() if n in name_set]
            missing = [n for n in names if n not in all_specs]
            if missing:
                available = ", ".join(all_specs.keys())
                raise KernelError(
                    f"Kernel spec(s) not found: {', '.join(missing)}. "
                    f"Available: {available}"
                )
            return results
        # General path: single-pass with early-continue filtering
        filtered: list[KernelSpec] = []
        filter_names = set(names) if names is not None else None
        for spec in all_specs.values():
            if kernel_type is not None and spec.kernel_type != kernel_type:
                continue
            if version is not None and spec.version != version:
                # Dynamic resolvers (http-dir, firecracker-s3) accept any
                # version — the spec's version is just the default.
                if spec.resolver in ("http-dir", "firecracker-s3"):
                    import copy as _copy

                    spec = _copy.copy(spec)
                    spec.version = version
                else:
                    continue
            if filter_names is not None and spec.name not in filter_names:
                continue
            filtered.append(spec)
        return filtered

    @classmethod
    def load_kernel_types_config(cls) -> list[dict[str, Any]]:
        """Load raw kernel type config dicts from kernels.yaml.

        Returns a list of config dicts in the same format as image types,
        suitable for passing to ``HttpDirVersionResolver.resolve()``.
        """
        specs = cls._load_specs()
        configs: list[dict[str, Any]] = []
        for spec in specs.values():
            config: dict[str, Any] = {
                "type": spec.kernel_type,
                "resolver": spec.resolver,
                "version": spec.version,
                "source": spec.source,
                "versions_url": spec.versions_url,
                "format": "tar.xz"
                if spec.kernel_type == "official"
                else "vmlinux",
                "name": spec.name,
            }
            if spec.list_url_template:
                config["list_url_template"] = spec.list_url_template
            if spec.sha256_url:
                config["sha256_url"] = spec.sha256_url
            if spec.options:
                config["options"] = spec.options
            if spec.resolver == "http-dir":
                config.setdefault("options", {})
                config["options"]["version_discoveries"] = (
                    spec.options.get("version_discoveries", [])
                    if spec.options
                    else []
                )
                config["options"]["file_pattern"] = (
                    spec.file_pattern or "linux-"
                )
                config["options"]["file_suffix"] = spec.file_suffix or ".tar.xz"
            elif spec.resolver == "firecracker-s3":
                config.setdefault("options", {})
                s3_pattern = (
                    spec.options.get("s3_version_pattern", "vmlinux-([\\d.]+)")
                    if spec.options
                    else "vmlinux-([\\d.]+)"
                )
                config["options"]["s3_version_pattern"] = s3_pattern
            configs.append(config)
        return configs

    @classmethod
    def resolve_latest_version(
        cls,
        kernel_type: str,
        ci_version: str | None = None,
    ) -> str:
        """Resolve 'latest' to the most recent version from upstream for the given kernel type.

        Args:
            kernel_type: Kernel type to resolve ('firecracker' or 'official').
            ci_version: Firecracker CI version for firecracker-s3 types.

        Returns:
            The highest available version string.

        Raises:
            KernelError: If the kernel type is unknown or no versions are available.
        """
        configs = cls.load_kernel_types_config()
        type_configs = [c for c in configs if c.get("type") == kernel_type]
        if not type_configs:
            raise KernelError(
                f"Cannot resolve 'latest' for unknown type: {kernel_type}"
            )

        version_map = HttpDirVersionResolver.resolve(
            type_configs,
            arch="x86_64",
            ci_version=ci_version,
            limit=1,
        )
        all_versions: list[str] = []
        for versions in version_map.values():
            for v in versions:
                all_versions.append(v.version)
        if not all_versions:
            raise KernelError(
                f"Cannot resolve 'latest' for "
                f"{kernel_type}: no versions available from upstream"
            )
        all_versions.sort(
            key=lambda v: tuple(int(x) for x in v.split(".")),
            reverse=True,
        )
        return all_versions[0]

    @classmethod
    def list_remote_versions(
        cls,
        specs: list[KernelSpec],
        *,
        arch: str,
        cache_ttl_seconds: int | None = None,
        ci_version: str | None = None,
    ) -> dict[str, list[VersionInfo]]:
        """List available remote kernel versions using the shared version resolver.

        Converts ``KernelSpec`` objects to config dicts that the shared
        ``HttpDirVersionResolver`` can consume, then fetches and returns
        version listings for all provided specs.

        Args:
            specs: List of KernelSpec to resolve remote versions for.
            arch: Target architecture.
            cache_ttl_seconds: TTL in seconds for HTTP response caching.
                ``None`` means no caching.
            ci_version: Firecracker CI version for firecracker-s3 types.

        Returns:
            Dict mapping kernel type name to sorted list of ``VersionInfo``
            (newest first).

        """
        configs: list[dict[str, Any]] = []
        for spec in specs:
            config: dict[str, Any] = {
                "type": spec.kernel_type,
                "format": "tar.xz"
                if spec.kernel_type == "official"
                else "vmlinux",
            }

            if spec.resolver == "http-dir" and spec.versions_url:
                config["resolver"] = "http-dir"
                config["versions_url"] = spec.versions_url

                opts: dict[str, object] = {}
                version_discoveries: list[str] = []
                if spec.options and isinstance(spec.options, dict):
                    raw_discoveries = spec.options.get(
                        "version_discoveries", []
                    )
                    if isinstance(raw_discoveries, list):
                        version_discoveries = [str(d) for d in raw_discoveries]
                opts["version_discoveries"] = version_discoveries
                opts["file_pattern"] = spec.file_pattern or "linux-"
                opts["file_suffix"] = spec.file_suffix or ".tar.xz"
                config["options"] = opts

                # Download URL template
                config["download_url"] = spec.source
                config["sha256_url"] = spec.sha256_url

            elif spec.resolver == "firecracker-s3" and spec.list_url_template:
                config["resolver"] = "firecracker-s3"
                config["source"] = spec.source
                # Strip {version} from list_url_template for listing purposes
                config["list_url_template"] = spec.list_url_template.replace(
                    "{version}", ""
                )
                # Download URL template without {version} placeholder for listing
                source_base = spec.source.rstrip("/")
                config["download_url"] = (
                    f"{source_base}/firecracker-ci/{{ci_version}}/{{arch}}/vmlinux-{{version}}"
                )
                if spec.sha256_url:
                    config["sha256_url"] = spec.sha256_url

                s3_opts: dict[str, object] = {}
                if spec.options and isinstance(spec.options, dict):
                    s3_opts["s3_version_pattern"] = spec.options.get(
                        "s3_version_pattern", "vmlinux-([\\d.]+)"
                    )
                else:
                    s3_opts["s3_version_pattern"] = "vmlinux-([\\d.]+)"
                config["options"] = s3_opts

            configs.append(config)

        return HttpDirVersionResolver.resolve(
            configs,
            arch=arch,
            cache_ttl_seconds=cache_ttl_seconds,
            ci_version=ci_version,
        )

    @staticmethod
    def parse_filename(filename: str) -> ParsedKernelFilename:
        """
        Parse a kernel filename to extract base name, version, and arch.
        Format: {base_name}-{version}[-{arch}]
        Examples:
          - vmlinux-firecracker-6.1.155-x86_64
          - vmlinux-5.10.0
        """
        name = filename
        arches = ["x86_64", "amd64", "arm64", "aarch64"]
        version = "-"
        arch = "-"
        # Step 1: Strip arch from the end (optional)
        for a in arches:
            if name.endswith(f"-{a}"):
                arch = a
                name = name[: -(len(a) + 1)]
                break
        # Step 2: Strip version from the end
        version_pattern = r"-v?(\d+(?:\.\d+)*)$"
        match = re.search(version_pattern, name)
        if match:
            version_num = match.group(1)
            full_match = match.group(0)
            version = (
                f"v{version_num}"
                if full_match.startswith("-v")
                else version_num
            )
            name = name[: match.start()]
        # Step 3: base_name is the first component (e.g. "vmlinux")
        base_name = name.split("-")[0]
        return ParsedKernelFilename(
            base_name=base_name, version=version, arch=arch
        )

    @staticmethod
    def download_kernel_source(
        url: str,
        dest: Path,
        sha256: str | None = None,
        progress_callback: Callable[[int, int | None], None] | None = None,
    ) -> Path:
        """Download kernel source tarball."""
        logger.info("Downloading kernel from %s", url)
        try:
            HttpDownload.download_file(
                url,
                dest,
                expected_sha256=sha256,
                timeout=HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
                progress_callback=progress_callback,
                allow_missing_checksum=sha256 is None,
                silent_missing_checksum=sha256 is None,
            )
        except ChecksumMismatchError:
            raise
        except MVMError as e:
            raise KernelError(f"Download failed: {e}") from e
        return dest

    @staticmethod
    def extract_kernel_tarball(
        tarball: Path,
        extract_dir: Path,
    ) -> Path:
        """Extract kernel tarball."""
        try:
            logger.info("Extracting %s...", tarball.name)
            with tarfile.open(tarball, "r:xz") as tar:
                tar.extractall(path=extract_dir, filter="data")
            # Find extracted directory (should be linux-X.Y.Z)
            for item in extract_dir.iterdir():
                if item.is_dir() and item.name.startswith("linux-"):
                    logger.info("Extracted to %s", item.name)
                    return item
            raise KernelError("Could not find extracted kernel directory")
        except tarfile.TarError as e:
            raise KernelError(f"Extraction failed: {e}") from e

    @staticmethod
    def _download_firecracker_config(
        kernel_dir: Path,
        spec: KernelSpec,
        arch: str,
        version: str,
    ) -> None:
        """Download Firecracker microvm kernel config."""
        if not spec.config_url_template:
            raise KernelError(
                f"Missing 'config_url_template' in kernels.yaml for {spec.name}"
            )
        major_minor = ".".join(version.split(".")[:2])
        template_vars = {
            "major_minor": major_minor,
            "version": major_minor,
            "arch": arch,
        }
        config_url = spec.config_url_template.format(**template_vars)
        try:
            logger.info("Downloading Firecracker kernel config...")
            config_content = HttpDownload.read_raw_content(
                config_url,
                timeout=HTTP_TIMEOUT_KERNEL_CONFIG_S,
                use_cache=True,
            )
            config_path = kernel_dir / ".config"
            config_path.write_text(config_content, encoding="utf-8")
            logger.info("Config downloaded")
        except HttpDownloadError as exc:
            raise KernelError(f"Failed to download config: {exc}") from exc

    @staticmethod
    def _run_make(
        kernel_dir: Path,
        target: str,
        jobs: int,
        capture_output: bool = True,
    ) -> tuple[int, str, str]:
        """Run make command in kernel directory.

        Output is captured by default to avoid dumping raw build noise
        (HOSTCC, LEX, etc.) to stdout. Config warnings are extracted
        from captured stderr and logged at info level.
        """
        cmd = ["make", target, f"-j{jobs}"]
        if capture_output:
            result = run_cmd(
                cmd,
                cwd=str(kernel_dir),
            )
            # Log config warnings from make output
            for line in (result.stderr or "").splitlines():
                stripped = line.strip()
                if ".config:" in stripped or "warning:" in stripped.lower():
                    logger.info("  %s", stripped)
            return result.returncode, result.stdout, result.stderr
        else:
            returncode = run_cmd(
                cmd,
                cwd=str(kernel_dir),
                capture=False,
            ).returncode
            return returncode, "", ""

    @staticmethod
    def _run_config_script(
        config_script: Path, args: list[str], kernel_dir: Path
    ) -> None:
        """Run scripts/config with the given args, logging a warning on failure."""
        result = run_cmd(
            [str(config_script)] + args,
            cwd=str(kernel_dir),
            check=False,
        )
        if result.returncode != 0:
            logger.warning(
                "scripts/config %s failed (rc=%d): %s",
                " ".join(args),
                result.returncode,
                result.stderr.strip(),
            )

    @staticmethod
    def _extract_config_key(config_line: str) -> str | None:
        line = config_line.strip()
        if not line:
            return None
        if line.startswith("# ") and line.endswith(" is not set"):
            key = line[2:-11]
            return key if key.startswith("CONFIG_") else None
        if line.startswith("CONFIG_") and "=" in line:
            return line.split("=", 1)[0]
        return None

    @classmethod
    def _merge_config_lines(cls, content: str, config_path: Path) -> None:
        """
        Merge config fragment content into an existing .config file.
        Uses line-by-line key replacement logic: existing keys are updated,
        new keys are appended.
        """
        existing_lines = config_path.read_text(encoding="utf-8").splitlines()
        key_to_index: dict[str, int] = {}
        for line_index, line in enumerate(existing_lines):
            key = cls._extract_config_key(line)
            if key:
                key_to_index[key] = line_index
        for fragment_line in content.splitlines():
            normalized = fragment_line.strip()
            key = cls._extract_config_key(normalized)
            if key is None:
                continue
            if key in key_to_index:
                existing_lines[key_to_index[key]] = normalized
            else:
                key_to_index[key] = len(existing_lines)
                existing_lines.append(normalized)
        merged_content = "\n".join(existing_lines)
        if merged_content:
            merged_content += "\n"
        config_path.write_text(merged_content, encoding="utf-8")

    @classmethod
    def _apply_config_fragments(
        cls,
        fragments: list[str],
        template_vars: dict[str, str],
        kernel_dir: Path,
        on_status: Callable[[str], None] | None = None,
    ) -> None:
        config_path = kernel_dir / ".config"
        for idx, fragment in enumerate(fragments):
            rendered = render_template(fragment, template_vars)
            if rendered.startswith("http://") or rendered.startswith(
                "https://"
            ):
                if on_status:
                    on_status(f"Fetching config fragment: {rendered}")
                try:
                    content = HttpDownload.read_raw_content(
                        rendered,
                        timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
                        use_cache=True,
                    )
                except HttpDownloadError as exc:
                    raise KernelError(
                        f"Failed to fetch config fragment {rendered}: {exc}"
                    ) from exc
                logger.info("Applying remote config fragment: %s", rendered)
            else:
                rel = (
                    rendered[len("assets/") :]
                    if rendered.startswith("assets/")
                    else rendered
                )
                if on_status:
                    on_status(f"Applying config fragment: {rel}")
                try:
                    content = AssetManager().read_file(rel)
                except Exception as exc:
                    raise KernelError(
                        f"Config fragment not found: {rel} (from '{fragment}')"
                    ) from exc
                logger.info("Applying local config fragment: %s", rel)
            if idx == 0 and not config_path.exists():
                base_content = (
                    content if content.endswith("\n") else f"{content}\n"
                )
                config_path.write_text(base_content, encoding="utf-8")
                continue
            if not config_path.exists():
                config_path.write_text("", encoding="utf-8")
            cls._merge_config_lines(content, config_path)

    @classmethod
    def prepare_kernel_config(
        cls,
        kernel_dir: Path,
        spec: KernelSpec,
        arch: str,
        *,
        jobs: int,
        user_config_path: Path | None = None,
        on_status: Callable[[str], None] | None = None,
    ) -> KernelConfigResult:
        """
        Configure kernel with Firecracker settings.
        Args:
            kernel_dir: Kernel source directory.
            spec: Resolved kernel specification.
            arch: Target architecture.
            jobs: Number of parallel make jobs.
            user_config_path: Optional custom config fragment to apply last.
        Returns:
            KernelConfigResult with status, warnings, and info messages.
        """
        warnings: list[str] = []
        info_messages: list[str] = []
        version = spec.version
        major_minor = ".".join(version.split(".")[:2])
        template_vars = {
            "major_minor": major_minor,
            "version": major_minor,
            "kernel_version": version,
            "ci_version": version,
            "arch": arch,
        }
        try:
            cls._download_firecracker_config(
                kernel_dir=kernel_dir,
                spec=spec,
                arch=arch,
                version=version,
            )
            if spec.config_fragments:
                if on_status:
                    on_status("Applying kernel config fragments...")
                cls._apply_config_fragments(
                    spec.config_fragments,
                    template_vars,
                    kernel_dir,
                    on_status=on_status,
                )
        except KernelError:
            if on_status:
                on_status("Using defconfig instead...")
            logger.info("Using defconfig instead...")
            returncode, _, _ = cls._run_make(kernel_dir, "defconfig", jobs=jobs)
            if returncode != 0:
                raise KernelError("defconfig failed")
        # Sync config to current kernel version
        if on_status:
            on_status("Synchronizing kernel config...")
        logger.info("Synchronizing config...")
        returncode, _, _ = cls._run_make(kernel_dir, "olddefconfig", jobs=jobs)
        if returncode != 0:
            raise KernelError("olddefconfig failed")
        config_script_path = kernel_dir / "scripts" / "config"
        if spec.enabled_configs:
            if on_status:
                on_status(
                    f"Enabling {len(spec.enabled_configs)} kernel options..."
                )
            logger.info("Applying kernel options from kernels.yaml...")
            for option in spec.enabled_configs:
                cls._run_config_script(
                    config_script_path, ["--enable", option], kernel_dir
                )
        if spec.disabled_configs:
            if on_status:
                on_status(
                    f"Disabling {len(spec.disabled_configs)} kernel options..."
                )
            logger.info("Applying disabled kernel options...")
            for option in spec.disabled_configs:
                cls._run_config_script(
                    config_script_path, ["--disable", option], kernel_dir
                )
        if spec.set_val_configs:
            if on_status:
                on_status(
                    f"Setting {len(spec.set_val_configs)} kernel options..."
                )
            logger.info("Applying set-val kernel options...")
            for option, value in spec.set_val_configs:
                cls._run_config_script(
                    config_script_path, ["--set-val", option, value], kernel_dir
                )
        if on_status:
            on_status("Resolving config dependencies...")
        logger.info("Resolving dependencies...")
        returncode, _, _ = cls._run_make(kernel_dir, "olddefconfig", jobs=jobs)
        if returncode != 0:
            raise KernelError("olddefconfig failed after enabling options")
        # Apply user config fragment if provided
        if user_config_path and user_config_path.exists():
            if on_status:
                on_status(f"Applying user config fragment: {user_config_path}")
            logger.info("Applying user config fragment: %s", user_config_path)
            config_path = kernel_dir / ".config"
            user_content = user_config_path.read_text(encoding="utf-8")
            cls._merge_config_lines(user_content, config_path)
            if on_status:
                on_status("Resolving dependencies after user config...")
            logger.info("Resolving dependencies after user config...")
            returncode, _, _ = cls._run_make(
                kernel_dir, "olddefconfig", jobs=jobs
            )
            if returncode != 0:
                raise KernelError("olddefconfig failed after user config")
        if on_status:
            on_status("Verifying kernel configuration...")
        logger.info("Verifying configuration...")
        config_path = kernel_dir / ".config"
        config_content = config_path.read_text()
        config_lines = set(config_content.splitlines())
        all_present = True
        missing_settings: list[str] = []
        for setting in spec.required_settings:
            if "=" in setting:
                present = setting in config_lines
            else:
                present = (
                    f"{setting}=y" in config_lines
                    or f"{setting}=m" in config_lines
                    or f"# {setting} is not set" in config_lines
                )
            if present:
                logger.info("  %s", setting)
            else:
                logger.error("  MISSING: %s", setting)
                missing_settings.append(setting)
                all_present = False
        if not all_present:
            warnings.append(
                f"Required kernel settings missing: {', '.join(missing_settings)}"
            )
            return KernelConfigResult(
                success=False,
                warnings=warnings,
                info_messages=info_messages,
            )
        return KernelConfigResult(
            success=True,
            warnings=warnings,
            info_messages=info_messages,
        )

    @classmethod
    def run_make_vmlinux(
        cls,
        kernel_dir: Path,
        output_path: Path,
        *,
        jobs: int,
    ) -> KernelBuildResult:
        """
        Build the kernel.
        Args:
            kernel_dir: Kernel source directory.
            output_path: Where to copy vmlinux.
            jobs: Number of parallel jobs.
        Returns:
            KernelBuildResult with status, warnings, and info messages.
        Raises:
            KernelError: If build fails.
        """
        logger.info("Building vmlinux with %d parallel jobs...", jobs)
        logger.info("This may take 10-30 minutes...")
        warnings: list[str] = []
        info_messages: list[str] = []
        warnings.append("Building kernel... (this may take 10-30 minutes)")
        cmd = ["make", "vmlinux", f"-j{jobs}"]
        build_log_path = output_path.parent / f"{output_path.name}.build.log"
        output_path.parent.mkdir(parents=True, exist_ok=True)
        try:
            with open(build_log_path, "w", encoding="utf-8") as log_file:
                proc = subprocess.Popen(
                    cmd,
                    cwd=kernel_dir,
                    stdout=log_file,
                    stderr=subprocess.STDOUT,
                )
                returncode = proc.wait()
            with open(build_log_path, encoding="utf-8") as f:
                for line in f:
                    line = line.rstrip("\n")
                    logger.debug("%s", line)
                    if _BUILD_LOG_PATTERNS.search(line):
                        warnings.append(line)
            if returncode != 0:
                raise KernelError(
                    f"Kernel build failed (exit {returncode}). Log: {build_log_path}"
                )
        except OSError as e:
            raise KernelError(
                "Kernel build failed: unable to execute make"
            ) from e
        # Copy vmlinux to output
        vmlinux_path = kernel_dir / "vmlinux"
        if not vmlinux_path.exists():
            raise KernelError("Build succeeded but vmlinux not found")
        shutil.copy2(vmlinux_path, output_path)
        output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)
        size = output_path.stat().st_size
        size_mb = size / CONST_MEBIBYTE_BYTES
        logger.info("Kernel built: %s (%.1f MiB)", output_path.name, size_mb)
        return KernelBuildResult(
            success=True,
            warnings=warnings,
            info_messages=info_messages,
        )

    @staticmethod
    def fetch_kernel_sha256_from_url(
        sha256_url: str, filename: str | None = None
    ) -> str | None:
        """Fetch SHA256 hash from a kernel.org SHA256SUMS.asc URL."""
        try:
            content = HttpDownload.read_raw_content(
                sha256_url,
                timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
                use_cache=True,
            ).strip()
            # If no filename specified, assume per-file sidecar format: "<hash>  <filename>"
            if filename is None:
                parts = content.split()
                return str(parts[0]).lower() if parts else None
            # Aggregated SHA256SUMS.asc format
            for line in content.split("\n"):
                line = line.strip()
                if (
                    not line
                    or line.startswith("-----")
                    or line.startswith("Hash:")
                ):
                    continue
                parts = line.split()
                if len(parts) >= 2 and parts[1] == filename:
                    return str(parts[0]).lower()
            return None
        except HttpDownloadError:
            return None

    @classmethod
    def _compute_config_hash(
        cls,
        spec: KernelSpec,
        version: str,
        user_config_path: Path | None = None,
    ) -> str:
        """Compute a hash of kernel configuration parameters for caching."""
        hasher = hashlib.sha256()
        hasher.update(version.encode())
        hasher.update(str(spec.config_fragments).encode())
        hasher.update(str(spec.enabled_configs).encode())
        hasher.update(str(spec.disabled_configs).encode())
        hasher.update(str(spec.set_val_configs).encode())
        hasher.update(str(spec.required_settings).encode())
        if user_config_path and user_config_path.exists():
            hasher.update(user_config_path.read_bytes())
        return hasher.hexdigest()[:16]

    @staticmethod
    def _try_cache_hit(
        output_path: Path,
        cache_marker: Path,
        cached_kernel_path: Path,
        use_cache: bool,
    ) -> bool:
        """
        Attempt to satisfy the build from cache.
        Returns True if a cache hit occurs and the kernel is available
        at ``output_path``.
        """
        if not use_cache:
            return False
        if cache_marker.exists() and cached_kernel_path.exists():
            shutil.copy2(cached_kernel_path, output_path)
            output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)
            logger.info(
                "Using cached kernel build (config hash match): %s", output_path
            )
            return True
        if output_path.exists() and cache_marker.exists():
            logger.info(
                "Using cached kernel (config hash match): %s", output_path
            )
            return True
        if output_path.exists():
            logger.info(
                "Kernel exists but config changed, rebuilding: %s", output_path
            )
            output_path.unlink(missing_ok=True)
        return False

    @classmethod
    def _resolve_source_and_checksum(
        cls,
        spec: KernelSpec,
        version: str,
        arch: str,
        sha256: str | None,
    ) -> tuple[str, str | None]:
        """
        Resolve source URL template vars and fetch SHA256 if needed.
        Returns:
            Tuple of (resolved_source_url, resolved_sha256).
        Raises:
            KernelError: If a checksum is required but cannot be resolved.
        """
        major = version.split(".")[0] if "." in version else version
        template_vars = {
            "version": version,
            "series": major,
            "kernel_version": version,
            "ci_version": version,
            "arch": arch,
        }
        resolved_source_url = (
            render_template(spec.source, template_vars)
            if "{" in spec.source
            else spec.source
        )
        intentional_no_checksum = (
            spec.sha256 is None and spec.sha256_url is None
        )
        resolved_sha256 = sha256
        if resolved_sha256 is None and not intentional_no_checksum:
            resolved_sha256_url = render_optional_template(
                spec.sha256_url, template_vars
            )
            if resolved_sha256_url is not None:
                filename = f"linux-{version}.tar.xz"
                resolved_sha256 = cls.fetch_kernel_sha256_from_url(
                    resolved_sha256_url, filename
                )
        if resolved_sha256 is None and not intentional_no_checksum:
            raise KernelError(
                f"Checksum required for kernel source download: {resolved_source_url}"
            )
        return resolved_source_url, resolved_sha256

    @classmethod
    def build_from_source(
        cls,
        spec: KernelSpec,
        version: str,
        source_url: str,
        output_path: Path,
        jobs: int,
        arch: str,
        *,
        sha256: str | None = None,
        keep_build_dir: bool = False,
        user_config_path: Path | None = None,
        use_cache: bool = True,
        progress_callback: Callable[[int, int | None], None] | None = None,
        on_status: Callable[[str], None] | None = None,
    ) -> KernelPipelineResult:
        """Orchestrate download → extract → configure → build."""
        # Compute config hash for caching
        build_dir = Path(spec.build_dir)
        config_hash = cls._compute_config_hash(spec, version, user_config_path)
        cache_key = f"{version}-{config_hash}"
        cache_marker = build_dir.parent / f"kernel-cache-{cache_key}.marker"
        cached_kernel_path = (
            build_dir.parent / f"kernel-cache-{cache_key}.vmlinux"
        )
        # 1. Cache hit?
        if cls._try_cache_hit(
            output_path, cache_marker, cached_kernel_path, use_cache
        ):
            return KernelPipelineResult(
                config_result=None,
                build_result=None,
                success=True,
            )
        # 2. Resolve source URL and checksum
        resolved_source_url, resolved_sha256 = cls._resolve_source_and_checksum(
            spec, version, arch, sha256
        )
        tarball = build_dir / f"linux-{version}.tar.xz"
        kernel_src_dir = build_dir / f"linux-{version}-{arch}"
        config_result: KernelConfigResult | None = None
        build_result: KernelBuildResult | None = None
        try:
            # 3. Download + extract
            if not tarball.exists():
                HttpDownload.download_file(
                    resolved_source_url,
                    tarball,
                    expected_sha256=resolved_sha256,
                    timeout=HTTP_TIMEOUT_KERNEL_DOWNLOAD_S,
                    allow_missing_checksum=resolved_sha256 is None,
                    silent_missing_checksum=resolved_sha256 is None,
                )
            else:
                logger.info("Using cached tarball: %s", tarball)
            if not kernel_src_dir.exists():
                extracted = cls.extract_kernel_tarball(tarball, build_dir)
                if extracted != kernel_src_dir:
                    extracted.rename(kernel_src_dir)
            else:
                logger.info("Using existing source: %s", kernel_src_dir)
            # 4. Prepare kernel config
            config_result = cls.prepare_kernel_config(
                kernel_src_dir,
                spec=spec,
                arch=arch,
                jobs=jobs,
                user_config_path=user_config_path,
                on_status=on_status,
            )
            # 5. Build vmlinux
            build_result = cls.run_make_vmlinux(
                kernel_src_dir, output_path, jobs=jobs
            )
            # 6. Cache output
            if use_cache:
                shutil.copy2(output_path, cached_kernel_path)
                cache_marker.write_text(cache_key)
        except Exception:
            raise
        else:
            if not keep_build_dir:
                try:
                    shutil.rmtree(build_dir)
                    logger.info("Build directory cleaned up: %s", build_dir)
                except OSError as exc:
                    logger.warning(
                        "Failed to clean up build directory %s: %s",
                        build_dir,
                        exc,
                    )
            else:
                logger.info("Build directory kept at: %s", build_dir)
        return KernelPipelineResult(
            config_result=config_result,
            build_result=build_result,
            success=True,
        )

    @classmethod
    def fetch_firecracker_kernel(
        cls,
        spec: KernelSpec,
        ci_version: str,
        arch: str,
        output_dir: Path,
        progress_callback: Callable[[int, int | None], None] | None = None,
    ) -> KernelPullResult:
        """
        Download Firecracker-compatible kernel from GitHub releases.

        Args:
            ci_version: Firecracker CI version string.
            arch: Target architecture.
            output_path: Destination path for the downloaded kernel.
        Returns:
            KernelPullResult with path, version, arch, type, warnings, info.
        """
        if not spec.list_url_template:
            raise KernelError(
                f"Missing 'list_url_template' in kernels.yaml for {spec.name}"
            )
        template_vars = {
            "ci_version": ci_version,
            "arch": arch,
            "version": spec.version,
        }
        list_url = render_template(spec.list_url_template, template_vars)
        try:
            xml_content = HttpDownload.read_raw_content(
                list_url,
                timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
                use_cache=True,
            )
        except HttpDownloadError as exc:
            raise KernelError(f"Failed to list CI kernels: {exc}") from exc
        pattern = rf"<Key>(firecracker-ci/{re.escape(ci_version)}/{re.escape(arch)}/vmlinux-[\d.]+)</Key>"
        keys = re.findall(pattern, xml_content)
        if not keys:
            raise KernelError(
                f"No vmlinux found for Firecracker CI version {ci_version} / arch {arch}"
            )
        keys.sort(
            key=lambda k: tuple(
                int(x) for x in k.split("/vmlinux-")[-1].split(".")
            )
        )
        chosen_key = keys[-1]
        kernel_version = chosen_key.split("/vmlinux-")[-1]
        output_path = output_dir / f"{spec.output_name}-{kernel_version}-{arch}"
        if output_path.exists():
            logger.info("Firecracker CI kernel already cached: %s", output_path)
            return KernelPullResult(
                path=output_path,
                version=kernel_version,
                arch=arch,
                kernel_type=KERNEL_TYPE_FIRECRACKER,
                warnings=[],
                info_messages=[f"Firecracker kernel ready: {output_path}"],
            )
        intentional_no_checksum = (
            spec.sha256 is None and spec.sha256_url is None
        )
        template_vars["kernel_version"] = kernel_version
        download_url = f"{spec.source.rstrip('/')}/{chosen_key}"
        sha256_url = render_optional_template(spec.sha256_url, template_vars)
        if sha256_url is None and not intentional_no_checksum:
            sha256_url = f"{download_url}.sha256"
        expected_sha256: str | None = None
        if sha256_url is not None:
            try:
                content = HttpDownload.read_raw_content(
                    sha256_url,
                    timeout=HTTP_TIMEOUT_SHA256_SIDECAR_S,
                    use_cache=True,
                ).strip()
                parts = content.split()
                expected_sha256 = str(parts[0]).lower() if parts else None
                logger.info("Fetched CI kernel checksum: %s", expected_sha256)
            except HttpDownloadError:
                logger.debug(
                    "No sha256 sidecar for CI kernel %s — proceeding without checksum",
                    chosen_key,
                )
        if expected_sha256 is None and not intentional_no_checksum:
            raise KernelError(
                f"Checksum required for Firecracker CI kernel download: {download_url}"
            )
        logger.info("Downloading Firecracker CI kernel from %s", download_url)
        try:
            HttpDownload.download_file(
                download_url,
                output_path,
                expected_sha256=expected_sha256,
                timeout=HTTP_TIMEOUT_SHA256_FETCH_S,
                progress_callback=progress_callback,
                allow_missing_checksum=True,
                silent_missing_checksum=True,
            )
        except MVMError as exc:
            raise KernelError(
                f"Failed to download Firecracker CI kernel: {exc}"
            ) from exc
        output_path.chmod(CONST_FILE_PERMS_EXECUTABLE)
        logger.info("Firecracker CI kernel saved: %s", output_path)
        return KernelPullResult(
            path=output_path,
            version=kernel_version,
            arch=arch,
            kernel_type=KERNEL_TYPE_FIRECRACKER,
            warnings=[],
            info_messages=[f"Firecracker kernel ready: {output_path}"],
        )

    @staticmethod
    def check_build_dependencies() -> list[str]:
        """
        Check for required kernel build dependencies.
        Returns:
            Empty list if all dependencies are present.
        Raises:
            KernelError: If any required dependency is missing.
        """
        required_commands = [
            "git",
            "curl",
            "make",
            "gcc",
            "flex",
            "bison",
            "bc",
            "pahole",
            "ld",
        ]
        missing_deps: list[str] = []
        for cmd in required_commands:
            if shutil.which(cmd) is None:
                missing_deps.append(cmd)
        library_checks = [
            ("libelf", "libelf"),
            ("openssl", "libssl-dev"),
        ]
        for pkg_name, display_name in library_checks:
            try:
                result = run_cmd(
                    ["pkg-config", "--exists", pkg_name],
                    check=False,
                )
                if result.returncode != 0:
                    missing_deps.append(display_name)
            except ProcessError:
                missing_deps.append(display_name)
        if missing_deps:
            missing_str = ", ".join(sorted(missing_deps))
            msg = (
                f"Missing kernel build dependencies: {missing_str}\n"
                "\n"
                "Install on Ubuntu/Debian:\n"
                "  sudo apt update\n"
                "  sudo apt install -y build-essential libncurses-dev bison flex\n"
                "  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves\n"
                "\n"
                "Install on Arch Linux:\n"
                "  sudo pacman -S base-devel ncurses bison flex\n"
                "  sudo pacman -S openssl bc curl git pahole\n"
            )
            raise KernelError(msg)
        return []

    @classmethod
    def build_official_kernel(
        cls,
        spec: KernelSpec,
        arch: str,
        output_dir: Path,
        jobs: int,
        *,
        keep_build_dir: bool = False,
        clean_build: bool = False,
        kernel_config: Path | None = None,
        progress_callback: Callable[[int, int | None], None] | None = None,
        on_status: Callable[[str], None] | None = None,
    ) -> KernelPullResult:
        """
        Build an official kernel from source.
        Args:
            spec: Resolved kernel specification.
            arch: Target architecture.
            output_path: Destination path for the built kernel.
            jobs: Number of parallel build jobs.
            keep_build_dir: Whether to retain build directory.
            clean_build: Whether to skip build cache.
            kernel_config: Optional custom config path.
        Returns:
            KernelPullResult with build results.
        """
        cls.check_build_dependencies()
        output_path = output_dir / f"{spec.output_name}-{spec.version}-{arch}"
        build_result = cls.build_from_source(
            version=spec.version,
            source_url=spec.source,
            output_path=output_path,
            sha256=spec.sha256,
            jobs=jobs,
            keep_build_dir=keep_build_dir,
            user_config_path=kernel_config,
            arch=arch,
            spec=spec,
            use_cache=not clean_build,
            progress_callback=progress_callback,
            on_status=on_status,
        )
        warnings: list[str] = []
        info_messages: list[str] = []
        if build_result.config_result:
            warnings.extend(build_result.config_result.warnings)
            info_messages.extend(build_result.config_result.info_messages)
        if build_result.build_result:
            warnings.extend(build_result.build_result.warnings)
            info_messages.extend(build_result.build_result.info_messages)
        info_messages.append(f"Kernel built: {output_path}")
        return KernelPullResult(
            path=output_path,
            version=spec.version,
            arch=arch,
            kernel_type=KERNEL_TYPE_OFFICIAL,
            warnings=warnings,
            info_messages=info_messages,
        )

    def import_kernel(
        self,
        name: str,
        source_path: Path,
        version: str,
        arch: str,
        set_default: bool = False,
    ) -> KernelItem:
        """Import a local vmlinux file as a kernel in the database.

        Copies the file to the kernels cache directory, generates a content-
        addressed SHA256 ID, creates a ``KernelItem`` with type ``"custom"``,
        and persists it via upsert.

        Args:
            name: User-assigned base name for this kernel entry.
            source_path: Path to the vmlinux file on disk.
            version: Kernel version string (already resolved from filename
                or user override by the caller).
            arch: Kernel architecture (already resolved from filename or
                user override by the caller).
            set_default: Whether to set as the default kernel after import.

        Returns:
            The created KernelItem.

        Raises:
            KernelError: If the source file cannot be read or copied.

        """
        resolved_path = source_path.expanduser().resolve()

        # Copy file to kernels cache directory
        dest_filename = f"{name}-{version}-{arch}"
        kernels_dir = CacheUtils.get_kernels_dir()
        dest_path = kernels_dir / dest_filename

        try:
            shutil.copy2(str(resolved_path), str(dest_path))
        except OSError as e:
            raise KernelError(
                f"Failed to copy kernel file to {dest_path}: {e}"
            ) from e
        dest_path.chmod(CONST_FILE_PERMS_EXECUTABLE)

        # Generate content-addressed ID
        timestamp = datetime.now(tz=UTC).isoformat()
        kernel_id = HashGenerator.kernel(dest_path, version, arch, timestamp)

        # Build KernelItem
        kernel_item = KernelItem(
            id=kernel_id,
            name=f"{name} {version}",
            base_name=name,
            version=version,
            arch=arch,
            type="custom",
            path=dest_filename,
            is_default=set_default,
            is_present=True,
            created_at=timestamp,
            updated_at=timestamp,
        )

        self._repo.upsert(kernel_item)
        if set_default:
            self._repo.set_default(kernel_item.id)

        logger.info(
            "Imported kernel: %s (version=%s, arch=%s, id=%s)",
            kernel_item.name,
            version,
            arch,
            HashGenerator.shorten(kernel_item.id),
        )
        return kernel_item
