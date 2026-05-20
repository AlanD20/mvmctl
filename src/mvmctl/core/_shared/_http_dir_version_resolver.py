"""Shared HTTP directory version resolver for image and kernel version listings.

Parses Apache-style HTML directory listings and S3 XML listings from upstream
providers to discover available versions. All provider-specific differences
(URLs, skip patterns, codename mapping, version prefixes) are expressed in
config dicts, not in code.

Three resolver strategies:
  - ``http-dir`` — Apache HTML directory listings
  - ``firecracker-s3`` — S3 bucket XML listings
  - ``single-source`` (resolver is ``None`` or ``""``) — single ``latest`` version
"""

from __future__ import annotations

import logging
import re
import xml.etree.ElementTree as ET
from typing import Any

from mvmctl.constants import DEFAULT_FIRECRACKER_CI_VERSION
from mvmctl.models.version import VersionInfo
from mvmctl.utils.http import HttpDownload
from mvmctl.utils.template import render_template

logger = logging.getLogger(__name__)


class HttpDirVersionResolver:
    """Resolves available versions from Apache HTML directory listings or S3 listings.

    Fetches version listing pages from upstream providers and parses them
    to extract available versions. Supports codename mapping, version prefix
    stripping, skip patterns, version discovery from subdirectories, and
    S3 bucket XML listing — all configured via config dicts rather than
    hardcoded in the resolver.

    Designed to be testable in isolation: given HTML/S3 XML input it
    returns structured ``VersionInfo`` objects. The only side effect
    is the HTTP fetch (which uses the project's existing cached HTTP
    infrastructure).
    """

    @staticmethod
    def resolve(
        configs: list[dict[str, Any]],
        *,
        arch: str,
        cache_ttl_seconds: int | None = None,
        ci_version: str | None = None,
        limit: int | None = None,
    ) -> dict[str, list[VersionInfo]]:
        """Fetch and parse version listings for all provided configs.

        Three resolver phases:

        **Phase 1 — http-dir resolver**
        For configs where ``resolver == "http-dir"`` and ``versions_url``
        is set. Fetches HTML directory listings and parses directory names
        or filenames depending on configuration.

        **Phase 2 — single-source (no resolver)**
        For configs where ``resolver`` is ``None`` or ``""``. Returns a
        single ``latest`` version constructed from URL templates.

        **Phase 3 — firecracker-s3 resolver**
        For configs where ``resolver == "firecracker-s3"`` and
        ``list_url_template`` is set. Fetches S3 bucket XML listings
        and extracts versions using a configurable version pattern.

        Args:
            configs: List of config dicts. Each config supports:
                - ``type`` (str): Type identifier.
                - ``resolver`` (str | None): Resolver strategy.
                - ``versions_url`` (str, optional): Root URL for http-dir.
                - ``download_url`` (str, optional): Download URL template.
                - ``sha256_url`` (str, optional): SHA256 URL template.
                - ``list_url_template`` (str, optional): S3 listing URL template.
                - ``format`` (str): Resource format.
                - ``options`` (dict, optional): Resolver-specific options.
                    http-dir options: ``skip_patterns``, ``version_prefix``,
                    ``codename_mapping``, ``arch_mapping``, ``file_discovery``,
                    ``version_discoveries``, ``file_pattern``, ``file_suffix``,
                    ``version_name_template``.
                    firecracker-s3 options: ``s3_version_pattern``.
                - ``name`` (str, optional): Display name for the type.
                - ``version_name_template`` (str, optional): Template for display names.
            arch: Target architecture (e.g. ``"x86_64"``, ``"aarch64"``).
            cache_ttl_seconds: TTL in seconds for HTTP response caching.
                ``None`` (default) means no caching — always fetch live.
            ci_version: Firecracker CI version for ``firecracker-s3`` types.
                Falls back to ``DEFAULT_FIRECRACKER_CI_VERSION`` if not provided.

        Returns:
            Dict mapping type name to sorted list of ``VersionInfo``
            (newest first). On fetch failure for a given type, returns
            an empty list for that type and logs a warning.

        """
        # Normalise cache TTL for HttpDownload (expects int, not None).
        # When cache_ttl_seconds is None, pass 0 as dummy since use_cache=False.
        _ttl: int = cache_ttl_seconds if cache_ttl_seconds is not None else 0

        result: dict[str, list[VersionInfo]] = {}

        # ── Phase 1: http-dir resolver types ──────────────────────────────
        for config in configs:
            if config.get("resolver") != "http-dir":
                continue

            type_name = config["type"]
            versions_url = config.get("versions_url")
            if not versions_url:
                continue

            options = config.get("options", {}) or {}
            version_discoveries = options.get("version_discoveries", []) or []

            if version_discoveries:
                # ── Version discovery from subdirectory file listings ──
                # versions_url is the ROOT. Each discovery is appended to
                # fetch and parse filenames from the listing.
                HttpDirVersionResolver._resolve_via_version_discoveries(
                    config=config,
                    type_name=type_name,
                    versions_url=versions_url,
                    version_discoveries=version_discoveries,
                    arch=arch,
                    cache_ttl_seconds=cache_ttl_seconds,
                    _ttl=_ttl,
                    result=result,
                )
            else:
                # ── Standard http-dir: parse directory names ───────────
                HttpDirVersionResolver._resolve_via_directory_listing(
                    config=config,
                    type_name=type_name,
                    versions_url=versions_url,
                    arch=arch,
                    cache_ttl_seconds=cache_ttl_seconds,
                    _ttl=_ttl,
                    result=result,
                )

        # ── Phase 2: single-source types with no resolver ──────────────
        for config in configs:
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
            opts = config.get("options", {}) or {}
            arch_mapping = opts.get("arch_mapping", {}) or {}
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

            sha256_url: str | None = None
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
                VersionInfo(
                    version="latest",
                    download_url=download_url,
                    sha256_url=sha256_url,
                    display_name=display_name,
                    type=type_name,
                    format=config.get("format", ""),
                )
            ]

        # ── Phase 3: firecracker-s3 resolver types ──────────────────────────
        for config in configs:
            resolver = config.get("resolver")
            if resolver != "firecracker-s3":
                continue

            type_name = config["type"]
            if type_name in result:
                continue  # Already added by earlier phase

            HttpDirVersionResolver._resolve_via_firecracker_s3(
                config=config,
                type_name=type_name,
                arch=arch,
                ci_version=ci_version,
                cache_ttl_seconds=cache_ttl_seconds,
                _ttl=_ttl,
                result=result,
            )

        # Apply global limit across all type groups if specified
        if limit is not None:
            for key in list(result):
                result[key] = result[key][:limit]

        return result

    # ── Phase 1 helpers ─────────────────────────────────────────────────

    @staticmethod
    def _resolve_via_directory_listing(
        config: dict[str, Any],
        type_name: str,
        versions_url: str,
        arch: str,
        cache_ttl_seconds: int | None,
        _ttl: int,
        result: dict[str, list[VersionInfo]],
    ) -> None:
        """Resolve versions by parsing directory names from an HTML listing."""
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
            return

        options = config.get("options", {}) or {}
        skip_patterns = options.get("skip_patterns", []) or []
        version_prefix = options.get("version_prefix")
        codename_mapping = options.get("codename_mapping", {}) or {}

        dirs = HttpDirVersionResolver._parse_directory_listing(html)

        config_name = config.get("name", "") or ""
        version_name_template = config.get("version_name_template")

        versions: list[VersionInfo] = []
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
            arch_mapping = options.get("arch_mapping", {}) or {}
            resolved_arch = arch_mapping.get(resolved_arch, resolved_arch)

            template_vars = {
                "version": version_str,
                "codename": codename if codename else "",
                "arch": resolved_arch,
            }

            try:
                download_url = render_template(
                    config.get("download_url", ""), template_vars
                )
            except (ValueError, KeyError) as exc:
                logger.warning(
                    "Failed to render download URL for %s version %s: %s",
                    type_name,
                    version_str,
                    exc,
                )
                continue

            sha256_url: str | None = None
            sha256_config = config.get("sha256_url")
            if sha256_config:
                try:
                    sha256_url = render_template(sha256_config, template_vars)
                except (ValueError, KeyError) as exc:
                    logger.warning(
                        "Failed to render sha256 URL for %s version %s: %s",
                        type_name,
                        version_str,
                        exc,
                    )

            # ── File discovery for directory-style download URLs ──
            file_discovery = options.get("file_discovery", {}) or {}
            if file_discovery.get("enabled") and download_url:
                discovered = HttpDirVersionResolver._discover_file_from_listing(
                    download_url,
                    pattern=file_discovery.get("pattern", "") or "",
                    suffix=file_discovery.get("suffix"),
                    cache_ttl_seconds=_ttl,
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
                VersionInfo(
                    version=version_str,
                    download_url=download_url,
                    sha256_url=sha256_url,
                    display_name=display_name,
                    type=type_name,
                    format=config.get("format", ""),
                )
            )

        versions.sort(
            key=HttpDirVersionResolver._version_sort_key,
            reverse=True,
        )

        limit = config.get("limit", 5)
        result[type_name] = versions[:limit]

    @staticmethod
    def _resolve_via_version_discoveries(
        config: dict[str, Any],
        type_name: str,
        versions_url: str,
        version_discoveries: list[str],
        arch: str,
        cache_ttl_seconds: int | None,
        _ttl: int,
        result: dict[str, list[VersionInfo]],
    ) -> None:
        """Resolve versions by scanning subdirectory file listings.

        Each discovery path becomes its own type group in the result dict,
        keyed as ``{type_name}-{discovery}`` (e.g. ``official-v6.x``).
        """
        options = config.get("options", {}) or {}
        file_pattern = options.get("file_pattern", "") or ""
        file_suffix = options.get("file_suffix", "") or ""
        version_name_template = config.get("version_name_template")
        config_name = config.get("name", "") or ""
        limit = options.get("limit", 5)

        for discovery in version_discoveries:
            discovery_key = f"{type_name}-{discovery.rstrip('/')}"
            discovery_versions: list[VersionInfo] = []

            # Ensure trailing slash on discovery path
            discovery_path = discovery.rstrip("/") + "/"
            discovery_url = versions_url.rstrip("/") + "/" + discovery_path

            try:
                html = HttpDownload.read_raw_content(
                    discovery_url,
                    use_cache=cache_ttl_seconds is not None,
                    cache_ttl_seconds=_ttl,
                )
            except Exception:
                logger.warning(
                    "Failed to fetch version listing for %s from %s",
                    type_name,
                    discovery_url,
                    exc_info=True,
                )
                continue

            # Extract ALL href links (files and directories)
            all_links: list[str] = re.findall(r'href="([^"]+)"', html)

            for link in all_links:
                # Skip directories (trailing /) and parent entries
                if link.endswith("/") or link in (".", "..", "../"):
                    continue
                if "?" in link or link.startswith("http"):
                    continue
                if link.startswith("/"):
                    continue

                # Filter by file_pattern and file_suffix
                if file_pattern and file_pattern not in link:
                    continue
                if file_suffix and not link.endswith(file_suffix):
                    continue

                # Extract version by stripping file_pattern and file_suffix
                version_str = link
                if file_pattern and version_str.startswith(file_pattern):
                    version_str = version_str[len(file_pattern) :]
                if file_suffix and version_str.endswith(file_suffix):
                    version_str = version_str[: -len(file_suffix)]

                if not version_str:
                    continue

                # Construct download URL from discovery URL + filename
                download_url = discovery_url.rstrip("/") + "/" + link

                # Construct sha256 URL if template exists
                sha256_url: str | None = None
                sha256_config = config.get("sha256_url")
                if sha256_config:
                    series = (
                        version_str.split(".")[0]
                        if "." in version_str
                        else version_str
                    )
                    template_vars = {
                        "version": version_str,
                        "series": series,
                        "arch": arch,
                    }
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

                display_name = f"{config_name} {version_str}".strip()
                if version_name_template:
                    try:
                        display_name = render_template(
                            version_name_template,
                            {
                                "version": version_str,
                                "series": version_str.split(".")[0]
                                if "." in version_str
                                else version_str,
                                "type": type_name,
                            },
                        )
                    except (ValueError, KeyError):
                        pass

                discovery_versions.append(
                    VersionInfo(
                        version=version_str,
                        download_url=download_url,
                        sha256_url=sha256_url,
                        display_name=display_name,
                        type=discovery_key,
                        format=config.get("format", ""),
                    )
                )

            discovery_versions.sort(
                key=HttpDirVersionResolver._version_sort_key,
                reverse=True,
            )
            result[discovery_key] = discovery_versions[:limit]

    # ── Phase 3 helper ───────────────────────────────────────────────────

    @staticmethod
    def _resolve_via_firecracker_s3(
        config: dict[str, Any],
        type_name: str,
        arch: str,
        ci_version: str | None,
        cache_ttl_seconds: int | None,
        _ttl: int,
        result: dict[str, list[VersionInfo]],
    ) -> None:
        """Resolve versions from an S3 bucket XML listing."""
        # Normalise cache TTL
        _ttl_int: int = (
            cache_ttl_seconds if cache_ttl_seconds is not None else 0
        )

        config_name = config.get("name", "") or ""
        version_name_template = config.get("version_name_template")
        download_url_tmpl = config.get("download_url", "")
        list_url_tmpl = config.get("list_url_template", "")

        if not list_url_tmpl:
            logger.debug(
                "Skipping %s: missing list_url_template",
                type_name,
            )
            result[type_name] = []
            return

        # Resolve ci_version — parameter overrides the default constant
        resolved_ci_version = ci_version or DEFAULT_FIRECRACKER_CI_VERSION

        # Options
        options = config.get("options", {}) or {}
        s3_version_pattern = options.get("s3_version_pattern", "([\\d.]+)")

        # Render S3 list URL from template
        list_vars = {
            "ci_version": resolved_ci_version,
            "arch": arch,
            "version": config.get("version", ""),
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
            return

        # Fetch S3 listing XML
        try:
            xml_content = HttpDownload.read_raw_content(
                list_url,
                use_cache=cache_ttl_seconds is not None,
                cache_ttl_seconds=_ttl_int,
            )
        except Exception:
            logger.warning(
                "Failed to fetch S3 version listing for %s from %s",
                type_name,
                list_url,
                exc_info=True,
            )
            result[type_name] = []
            return

        # Parse XML and extract versions from S3 keys
        try:
            root = ET.fromstring(xml_content)
        except ET.ParseError:
            logger.warning(
                "Failed to parse S3 XML for %s", type_name, exc_info=True
            )
            result[type_name] = []
            return

        # S3 ListBucketResult namespace
        ns = {"s3": "http://s3.amazonaws.com/doc/2006-03-01/"}

        s3_versions: list[VersionInfo] = []
        seen_versions: set[str] = set()

        for contents in root.findall(".//s3:Contents", ns):
            key_elem = contents.find("s3:Key", ns)
            if key_elem is None or key_elem.text is None:
                continue

            key = key_elem.text
            # Extract version from key using configured pattern
            match = re.search(s3_version_pattern, key)
            if not match:
                continue

            version_str = match.group(1).rstrip(".")
            if version_str in seen_versions:
                continue  # Deduplicate across S3 keys
            seen_versions.add(version_str)

            # Render download URL from template
            download_vars = {
                "ci_version": resolved_ci_version,
                "arch": arch,
                "version": version_str,
            }
            if download_url_tmpl:
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
            else:
                # Construct from source + key
                source = config.get("source", "")
                download_url = f"{source.rstrip('/')}/{key}"

            # Render sha256 URL from template if configured
            sha256_url: str | None = None
            sha256_config = config.get("sha256_url")
            if sha256_config:
                try:
                    sha256_url = render_template(sha256_config, download_vars)
                except (ValueError, KeyError) as exc:
                    logger.warning(
                        "Failed to render sha256 URL for %s version %s: %s",
                        type_name,
                        version_str,
                        exc,
                    )

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
                VersionInfo(
                    version=version_str,
                    download_url=download_url,
                    sha256_url=sha256_url,
                    display_name=display_name,
                    type=type_name,
                    format=config.get("format", ""),
                )
            )

        s3_versions.sort(
            key=HttpDirVersionResolver._version_sort_key,
            reverse=True,
        )

        limit = config.get("limit", 5)
        result[type_name] = s3_versions[:limit]

    # ── Utility methods ─────────────────────────────────────────────────

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
    def _version_sort_key(entry: VersionInfo) -> tuple[int | float, ...]:
        """Sort key for ``VersionInfo``, supporting dotted numeric versions.

        Splits version by ``.``, converts each part to int. Falls back
        to ``(0,)`` for non-numeric versions so they sort to the end.

        """
        parts = entry.version.split(".")
        try:
            return tuple(int(p) for p in parts)
        except ValueError:
            return (0,)
