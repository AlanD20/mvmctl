"""HTTP download utilities."""

import hashlib
import logging
from pathlib import Path
from urllib.request import urlopen, Request
from urllib.error import URLError

from fcm.constants import HTTP_USER_AGENT
from fcm.exceptions import FCMError, ChecksumMismatchError

logger = logging.getLogger(__name__)

_DOWNLOAD_CHUNK_SIZE = 524288


def download_file(
    url: str,
    dest: Path,
    expected_sha256: str | None = None,
    show_progress: bool = True,
    timeout: int = 300,
) -> bool:
    """Download a file with optional progress display and checksum verification.

    Args:
        url: URL to download from
        dest: Destination path
        expected_sha256: Optional SHA-256 checksum to verify
        show_progress: Show progress via logging
        timeout: Request timeout in seconds

    Returns:
        True if successful

    Raises:
        FCMError: On download or I/O failure
        ChecksumMismatchError: On checksum mismatch
    """
    dest.parent.mkdir(parents=True, exist_ok=True)

    if expected_sha256 is None:
        logger.warning("No checksum provided for download: %s", url)

    try:
        req = Request(url, headers={"User-Agent": HTTP_USER_AGENT})

        if show_progress:
            logger.info("Downloading %s", url)

        with urlopen(req, timeout=timeout) as response:
            total_size = response.headers.get("Content-Length")

            sha256_hash = hashlib.sha256() if expected_sha256 else None
            downloaded = 0

            with open(dest, "wb") as f:
                while True:
                    chunk = response.read(_DOWNLOAD_CHUNK_SIZE)
                    if not chunk:
                        break
                    f.write(chunk)
                    downloaded += len(chunk)

                    if sha256_hash:
                        sha256_hash.update(chunk)

                    if show_progress and total_size:
                        percent = (downloaded / int(total_size)) * 100
                        logger.debug("Progress: %.1f%%", percent)

        # Verify checksum if provided
        if expected_sha256 and sha256_hash:
            actual_sha256 = sha256_hash.hexdigest()
            if actual_sha256.lower() != expected_sha256.lower():
                dest.unlink()
                raise ChecksumMismatchError(
                    f"Checksum mismatch! Expected {expected_sha256}, got {actual_sha256}"
                )
            logger.info("Checksum verified")

        return True

    except URLError as e:
        raise FCMError(f"Download failed: {e}") from e
    except IOError as e:
        raise FCMError(f"I/O error: {e}") from e
