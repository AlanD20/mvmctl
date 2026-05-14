"""HTTP directory version resolver for image type version listings.

Parses Apache-style HTML directory listings from upstream image providers
to discover available image versions. All provider-specific differences
(URLs, skip patterns, codename mapping, version prefixes) are expressed
in the ``image_types`` YAML config, not in code.
"""

from __future__ import annotations

import logging
import re
import xml.etree.ElementTree as ET
from typing import Any

from mvmctl.constants import (
    CONST_IMAGE_VERSION_LIST_LIMIT,
    DEFAULT_FIRECRACKER_CI_VERSION,
)
from mvmctl.models.image import ImageVersion
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_template

logger = logging.getLogger(__name__)


class HttpDirVersionResolver:
    """Resolves available versions from Apache HTML directory listings.

    Fetches version listing pages from upstream image providers and parses
    the HTML to extract available versions. Supports codename mapping,
    version prefix stripping, and skip patterns — all configured via the
    image_types YAML config rather than hardcoded in the resolver.

    This is designed to be testable in isolation: given HTML input it
    returns structured ``ImageVersion`` objects. The only side effect
    is the HTTP fetch (which uses the project's existing cached HTTP
    infrastructure).

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

        For each image type config with ``resolver: http-dir``, fetches
        the ``versions_url``, parses the HTML directory listing, filters
        with ``skip_patterns``, resolves versions via codename mapping
        or version prefix, renders download/sha256 URLs from templates,
        sorts descending, and limits to ``CONST_IMAGE_VERSION_LIST_LIMIT``.

        Types with other resolvers (``firecracker-s3``) or no resolver
        (``archlinux``) are resolved by their own mechanisms.

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
        # Normalise cache TTL for HttpDownload (expects int, not None).
        # When cache_ttl_seconds is None, pass 0 as dummy since use_cache=False.
        _ttl: int = cache_ttl_seconds if cache_ttl_seconds is not None else 0

        result: dict[str, list[ImageVersion]] = {}

        # ── Phase 1: http-dir resolver types ──────────────────────────────
        for config in image_types_config:
            if config.get("resolver") != "http-dir":
                continue

            type_name = config["type"]
            versions_url = config.get("versions_url")
            if not versions_url:
                continue

            try:
                html = HttpDownload.read_raw_content(
                    versions_url,
                    use_cache=cache_ttl_seconds is not None,
                    cache_ttl_seconds=_ttl,
                )
            except Exception:
                logger.warning(
                    "Failed to fetch version listing for %s from %s",
                    type_name,
                    versions_url,
                    exc_info=True,
                )
                result[type_name] = []
                continue

            options = config.get("options", {}) or {}
            skip_patterns = options.get("skip_patterns", []) or []
            version_prefix = options.get("version_prefix")
            codename_mapping = options.get("codename_mapping", {}) or {}

            dirs = HttpDirVersionResolver._parse_directory_listing(html)

            config_name = config.get("name", "") or ""
            version_name_template = config.get("version_name_template")

            versions: list[ImageVersion] = []
            for dir_name in dirs:
                parsed = HttpDirVersionResolver._resolve_version(
                    dir_name,
                    skip_patterns=skip_patterns,
                    version_prefix=version_prefix,
                    codename_mapping=codename_mapping,
                )
                if parsed is None:
                    continue

                version_str, codename = parsed

                resolved_arch = arch
                # Apply arch_mapping if configured (e.g., x86_64 -> amd64)
                arch_mapping = options.get("arch_mapping", {}) or {}
                resolved_arch = arch_mapping.get(resolved_arch, resolved_arch)
                template_vars = {
                    "version": version_str,
                    "codename": codename if codename else "",
                    "arch": resolved_arch,
                }

                try:
                    download_url = render_template(
                        config["download_url"], template_vars
                    )
                except (ValueError, KeyError) as exc:
                    logger.warning(
                        "Failed to render download URL for %s version %s: %s",
                        type_name,
                        version_str,
                        exc,
                    )
                    continue

                sha256_url = None
                sha256_config = config.get("sha256_url")
                if sha256_config:
                    try:
                        sha256_url = render_template(
                            sha256_config, template_vars
                        )
                    except (ValueError, KeyError) as exc:
                        logger.warning(
                            "Failed to render sha256 URL for %s version %s: %s",
                            type_name,
                            version_str,
                            exc,
                        )
                        sha256_url = None

                # ── File discovery for directory-style download URLs ──
                # Some providers (e.g., Alpine) list versions as directories
                # containing multiple files. We fetch the directory listing
                # and find the actual file matching our pattern/suffix.
                file_discovery = options.get("file_discovery", {}) or {}
                if file_discovery.get("enabled") and download_url:
                    discovered = (
                        HttpDirVersionResolver._discover_file_from_listing(
                            download_url,
                            pattern=file_discovery.get("pattern", "") or "",
                            suffix=file_discovery.get("suffix"),
                            cache_ttl_seconds=_ttl,
                        )
                    )
                    if discovered:
                        download_url = discovered
                        sha256_suffix_cfg = file_discovery.get("sha256_suffix")
                        if sha256_suffix_cfg:
                            sha256_url = download_url + sha256_suffix_cfg
                    else:
                        logger.debug(
                            "No matching cloud image for %s version %s",
                            type_name,
                            version_str,
                        )
                        continue

                # ── Build display_name from template or fallback ──────
                if version_name_template:
                    try:
                        display_name = render_template(
                            version_name_template,
                            {
                                "version": version_str,
                                "codename": codename if codename else "",
                                "type": type_name,
                            },
                        )
                    except (ValueError, KeyError):
                        display_name = f"{config_name} {version_str}".strip()
                else:
                    display_name = f"{config_name} {version_str}".strip()

                versions.append(
                    ImageVersion(
                        version=version_str,
                        codename=codename,
                        type=type_name,
                        download_url=download_url,
                        sha256_url=sha256_url,
                        format=config["format"],
                        display_name=display_name,
                        type_name=config_name,
                    )
                )

            versions.sort(
                key=HttpDirVersionResolver._version_sort_key,
                reverse=True,
            )

            limit = CONST_IMAGE_VERSION_LIST_LIMIT
            result[type_name] = versions[:limit]

        # ── Phase 2: single-source types with no resolver ──────────────
        # Types like archlinux have resolver: null and a direct download_url.
        # They don't do version discovery — they have a single "latest" version.
        for config in image_types_config:
            resolver = config.get("resolver")
            if resolver is not None and resolver != "":
                continue  # Already handled (http-dir) or handled elsewhere

            type_name = config["type"]
            if type_name in result:
                continue  # Already added by Phase 1

            download_url_tmpl = config.get("download_url")
            if not download_url_tmpl:
                continue

            resolved_arch = arch
            options = config.get("options", {}) or {}
            arch_mapping = options.get("arch_mapping", {}) or {}
            resolved_arch = arch_mapping.get(resolved_arch, resolved_arch)

            template_vars = {
                "version": "latest",
                "codename": "",
                "arch": resolved_arch,
            }

            try:
                download_url = render_template(download_url_tmpl, template_vars)
            except (ValueError, KeyError) as exc:
                logger.warning(
                    "Failed to render download URL for %s: %s",
                    type_name,
                    exc,
                )
                continue

            sha256_url = None
            sha256_config = config.get("sha256_url")
            if sha256_config:
                try:
                    sha256_url = render_template(sha256_config, template_vars)
                except (ValueError, KeyError) as exc:
                    logger.warning(
                        "Failed to render sha256 URL for %s: %s",
                        type_name,
                        exc,
                    )

            config_name = config.get("name", "") or ""
            version_name_template = config.get("version_name_template")
            if version_name_template:
                try:
                    display_name = render_template(
                        version_name_template,
                        {
                            "version": "latest",
                            "codename": "",
                            "type": type_name,
                        },
                    )
                except (ValueError, KeyError):
                    display_name = config_name
            else:
                display_name = config_name

            result[type_name] = [
                ImageVersion(
                    version="latest",
                    codename=None,
                    type=type_name,
                    download_url=download_url,
                    sha256_url=sha256_url,
                    format=config["format"],
                    display_name=display_name,
                    type_name=config_name,
                )
            ]

        # ── Phase 3: firecracker-s3 resolver types ──────────────────────────
        # Types like firecracker use S3 bucket XML listing for version discovery.
        for config in image_types_config:
            resolver = config.get("resolver")
            if resolver != "firecracker-s3":
                continue

            type_name = config["type"]
            if type_name in result:
                continue  # Already added by Phase 1 or Phase 2

            config_name = config.get("name", "") or ""
            version_name_template = config.get("version_name_template")
            download_url_tmpl = config.get("download_url", "")
            list_url_tmpl = config.get("list_url_template", "")

            if not list_url_tmpl or not download_url_tmpl:
                logger.debug(
                    "Skipping %s: missing list_url_template or download_url",
                    type_name,
                )
                result[type_name] = []
                continue

            # Resolve ci_version — parameter overrides the default constant
            resolved_ci_version = ci_version or DEFAULT_FIRECRACKER_CI_VERSION

            # Resolve arch
            resolved_arch = arch

            # Render S3 list URL from template
            list_vars = {
                "ci_version": resolved_ci_version,
                "arch": resolved_arch,
            }
            try:
                list_url = render_template(list_url_tmpl, list_vars)
            except (ValueError, KeyError) as exc:
                logger.warning(
                    "Failed to render S3 list URL for %s: %s",
                    type_name,
                    exc,
                )
                result[type_name] = []
                continue

            # Fetch S3 listing XML
            try:
                xml_content = HttpDownload.read_raw_content(
                    list_url,
                    use_cache=cache_ttl_seconds is not None,
                    cache_ttl_seconds=_ttl,
                )
            except Exception:
                logger.warning(
                    "Failed to fetch S3 version listing for %s from %s",
                    type_name,
                    list_url,
                    exc_info=True,
                )
                result[type_name] = []
                continue

            # Parse XML and extract ubuntu versions from S3 keys
            try:
                root = ET.fromstring(xml_content)
            except ET.ParseError:
                logger.warning(
                    "Failed to parse S3 XML for %s", type_name, exc_info=True
                )
                result[type_name] = []
                continue

            # S3 ListBucketResult namespace
            ns = {"s3": "http://s3.amazonaws.com/doc/2006-03-01/"}

            s3_versions: list[ImageVersion] = []
            seen_versions: set[str] = set()

            for contents in root.findall(".//s3:Contents", ns):
                key_elem = contents.find("s3:Key", ns)
                if key_elem is None or key_elem.text is None:
                    continue

                key = key_elem.text
                # Extract ubuntu version from key pattern:
                # firecracker-ci/v1.15/x86_64/ubuntu-24.04.squashfs
                match = re.search(r"ubuntu-([0-9.]+)\.squashfs", key)
                if not match:
                    continue

                version_str = match.group(1)
                if version_str in seen_versions:
                    continue  # Deduplicate across S3 keys
                seen_versions.add(version_str)

                # Render download URL from template
                download_vars = {
                    "ci_version": resolved_ci_version,
                    "arch": resolved_arch,
                    "version": version_str,
                }
                try:
                    download_url = render_template(
                        download_url_tmpl, download_vars
                    )
                except (ValueError, KeyError) as exc:
                    logger.warning(
                        "Failed to render download URL for %s version %s: %s",
                        type_name,
                        version_str,
                        exc,
                    )
                    continue

                # Build display_name from template or fallback
                if version_name_template:
                    try:
                        display_name = render_template(
                            version_name_template,
                            {
                                "version": version_str,
                                "ci_version": resolved_ci_version,
                                "type": type_name,
                            },
                        )
                    except (ValueError, KeyError):
                        display_name = f"{config_name} {version_str}".strip()
                else:
                    display_name = f"{config_name} {version_str}".strip()

                s3_versions.append(
                    ImageVersion(
                        version=version_str,
                        codename=None,
                        type=type_name,
                        download_url=download_url,
                        sha256_url=None,
                        format=config["format"],
                        display_name=display_name,
                        type_name=config_name,
                    )
                )

            s3_versions.sort(
                key=HttpDirVersionResolver._version_sort_key,
                reverse=True,
            )

            limit = CONST_IMAGE_VERSION_LIST_LIMIT
            result[type_name] = s3_versions[:limit]

        return result

    @staticmethod
    def _parse_directory_listing(html: str) -> list[str]:
        """Extract directory names from Apache HTML directory listing.

        Apache-style listings use ``<a href="dirname/">`` anchors for
        subdirectories. Returns the extracted directory names without
        trailing slashes.

        Args:
            html: Raw HTML content of the directory listing page.

        Returns:
            List of directory name strings (e.g. ``["24.04", "22.04"]``).

        """
        # Use dict.fromkeys to deduplicate while preserving insertion order
        # (Apache autoindex lists each directory twice — header + body row)
        return list(dict.fromkeys(re.findall(r'href="([^"]+)/"', html)))

    @staticmethod
    def _discover_file_from_listing(
        url: str,
        *,
        pattern: str,
        suffix: str | None = None,
        cache_ttl_seconds: int | None = None,
    ) -> str | None:
        """Fetch a directory listing HTML and find a matching file URL.

        Parses ``<a href="...">`` anchors from Apache-style directory listings,
        filtering for files that match the given ``pattern`` and optional
        ``suffix``. Directories (trailing ``/``) and parent entries are skipped.

        Args:
            url: Directory listing URL to fetch.
            pattern: Required substring in the filename.
            suffix: Optional additional required substring.
            cache_ttl_seconds: TTL for HTTP response caching. ``None`` (default)
                means no caching.

        Returns:
            Full URL of the first matching file, or ``None`` if no match
            or the fetch fails.

        """
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

        # Normalise base URL — ensure trailing slash
        base = url.rstrip("/") + "/"

        for link in all_links:
            # Skip directories, parent entries, and non-file links
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
        """Resolve a directory name to a (version, codename) pair.

        Applies filtering and resolution rules in order:

        1. Skip ``.`` and ``..`` entries.
        2. Skip directories matching any ``skip_pattern``.
        3. If ``codename_mapping`` is non-empty, look up dir_name
           (as codename) to get the version — skip if not found.
        4. If ``version_prefix`` is set, strip it from dir_name
           to get the version — skip if prefix doesn't match.
        5. Otherwise use dir_name directly as the version.

        The ``codename`` return value is ``None`` for numeric version
        directories and set to the original dir_name for codename-mapped
        directories.

        Args:
            dir_name: Directory name extracted from HTML listing.
            skip_patterns: Patterns to exclude (substring match).
            version_prefix: Optional prefix to strip (e.g. ``v``).
            codename_mapping: Optional mapping of codename to version.

        Returns:
            ``(version, codename)`` tuple if the dir resolves to a valid
            version, or ``None`` if it should be skipped.

        """
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
        """Sort key for ``ImageVersion``, supporting dotted numeric versions.

        Splits version by ``.``, converts each part to int. Falls back
        to ``(0,)`` for non-numeric versions so they sort to the end.

        """
        parts = entry.version.split(".")
        try:
            return tuple(int(p) for p in parts)
        except ValueError:
            return (0,)
