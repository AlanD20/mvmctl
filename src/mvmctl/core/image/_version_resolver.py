"""HTTP directory version resolver for image type version listings (wrapper).

This module is now a thin wrapper around the shared
:class:`mvmctl.core._shared._http_dir_version_resolver.HttpDirVersionResolver`.
It converts the generic ``VersionInfo`` objects to ``ImageVersion`` objects
for backward compatibility with existing image consumers.
"""

from __future__ import annotations

import logging
import re
from typing import Any

from mvmctl.core._shared._http_dir_version_resolver import (
    HttpDirVersionResolver as _SharedResolver,
)
from mvmctl.models.image import ImageVersion
from mvmctl.utils.http import HttpDownload

logger = logging.getLogger(__name__)


class HttpDirVersionResolver:
    """Resolves available image versions from HTML or S3 listings.

    Thin wrapper around the shared resolver that converts ``VersionInfo``
    results to ``ImageVersion`` for backward compatibility.

    This class preserves the original public API so existing callers
    (``ImageService.get_specs_for``, ``ImageOperation.list_``) continue
    to work without changes.
    """

    @staticmethod
    def resolve(
        image_types_config: list[dict[str, Any]],
        *,
        arch: str,
        cache_ttl_seconds: int | None = None,
        ci_version: str | None = None,
    ) -> dict[str, list[ImageVersion]]:
        """Fetch and parse version listings for all http-dir image types.

        Delegates to the shared resolver, then converts each ``VersionInfo``
        to an ``ImageVersion`` (adding image-specific fields like ``codename``
        and ``type_name``).

        Args:
            image_types_config: List of image type config dicts from
                the ``image_types`` key in ``images.yaml``.
            arch: Target architecture (e.g. ``"x86_64"``, ``"aarch64"``).
            cache_ttl_seconds: TTL in seconds for HTTP response caching.
                ``None`` (default) means no caching — always fetch live.

        Returns:
            Dict mapping type name to sorted list of ``ImageVersion``
            (newest first). On fetch failure for a given type, returns
            an empty list for that type and logs a warning.

        """
        raw = _SharedResolver.resolve(
            image_types_config,
            arch=arch,
            cache_ttl_seconds=cache_ttl_seconds,
            ci_version=ci_version,
        )

        # Convert dict[str, list[VersionInfo]] to dict[str, list[ImageVersion]]
        result: dict[str, list[ImageVersion]] = {}
        for type_name, versions in raw.items():
            image_versions: list[ImageVersion] = []
            for v in versions:
                # Find the original config for this type to get type_name
                config = _find_config(image_types_config, type_name)
                config_name = config.get("name", "") if config else ""

                # Determine codename from version_name_template context
                codename: str | None = None
                if config:
                    options = config.get("options", {}) or {}
                    codename_mapping = options.get("codename_mapping", {}) or {}
                    # Reverse lookup: find if any codename maps to this version
                    for (
                        codename_key,
                        mapped_version,
                    ) in codename_mapping.items():
                        if mapped_version == v.version:
                            codename = codename_key
                            break

                image_versions.append(
                    ImageVersion(
                        version=v.version,
                        codename=codename,
                        type=v.type,
                        download_url=v.download_url,
                        sha256_url=v.sha256_url,
                        format=v.format,
                        display_name=v.display_name,
                        type_name=config_name,
                    )
                )
            result[type_name] = image_versions

        return result

    # ── Preserved utility methods (used by other parts of the image code) ──

    @staticmethod
    def _parse_directory_listing(html: str) -> list[str]:
        """Extract directory names from Apache HTML directory listing."""
        # Use dict.fromkeys to deduplicate while preserving insertion order
        return list(dict.fromkeys(re.findall(r'href="([^"]+)/"', html)))

    @staticmethod
    def _discover_file_from_listing(
        url: str,
        *,
        pattern: str,
        suffix: str | None = None,
        cache_ttl_seconds: int | None = None,
    ) -> str | None:
        """Fetch a directory listing HTML and find a matching file URL."""
        _ttl: int = cache_ttl_seconds if cache_ttl_seconds is not None else 0
        try:
            html = HttpDownload.read_raw_content(
                url,
                use_cache=cache_ttl_seconds is not None,
                cache_ttl_seconds=_ttl,
            )
        except Exception:
            logger.debug(
                "File discovery directory not available: %s (skipping)",
                url,
            )
            return None

        all_links: list[str] = re.findall(r'href="([^"]+)"', html)
        base = url.rstrip("/") + "/"

        for link in all_links:
            if link.endswith("/") or link in (".", "..", "../"):
                continue
            if "?" in link or link.startswith("http"):
                continue
            if pattern in link:
                if suffix is None or suffix in link:
                    return base + link

        return None

    @staticmethod
    def _resolve_version(
        dir_name: str,
        *,
        skip_patterns: list[str],
        version_prefix: str | None,
        codename_mapping: dict[str, str],
    ) -> tuple[str, str | None] | None:
        """Resolve a directory name to a (version, codename) pair."""
        if dir_name in (".", ".."):
            return None

        if any(pattern in dir_name for pattern in skip_patterns):
            return None

        if codename_mapping:
            version = codename_mapping.get(dir_name)
            if version is None:
                return None
            return (version, dir_name)

        if version_prefix:
            if not dir_name.startswith(version_prefix):
                return None
            return (dir_name[len(version_prefix) :], None)

        return (dir_name, None)

    @staticmethod
    def _version_sort_key(entry: ImageVersion) -> tuple[int | float, ...]:
        """Sort key for ``ImageVersion``, supporting dotted numeric versions."""
        parts = entry.version.split(".")
        try:
            return tuple(int(p) for p in parts)
        except ValueError:
            return (0,)


def _find_config(
    configs: list[dict[str, Any]], type_name: str
) -> dict[str, Any] | None:
    """Find the config dict for a given type name."""
    for config in configs:
        if config.get("type") == type_name:
            return config
    return None
