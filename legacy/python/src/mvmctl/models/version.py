"""Version data models.

Shared between kernel and image version resolution — published versions
discovered from upstream providers.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class VersionInfo:
    """A published version of a resource from an upstream provider.

    Returned by version resolvers to describe an available download.
    Generic enough for both image and kernel version listings.
    """

    version: str
    download_url: str
    sha256_url: str | None
    display_name: str
    type: str
    format: str
