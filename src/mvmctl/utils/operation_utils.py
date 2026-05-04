"""Operation utilities — bridges between raw progress and UI events."""

from __future__ import annotations

import time
from collections.abc import Callable
from typing import TYPE_CHECKING

from mvmctl.utils.common import CommonUtils

if TYPE_CHECKING:
    from mvmctl.models.result import ProgressEvent


class OperationUtils:
    """
    Domain-agnostic operation helpers.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def download_progress_bridge(
        on_progress: Callable[[ProgressEvent], None] | None,
    ) -> Callable[[int, int | None], None] | None:
        """
        Bridge raw HTTP download progress into :class:`ProgressEvent` emissions.

        Takes a domain-level ``on_progress`` callback (that consumes
        :class:`ProgressEvent`) and returns a ``(current_bytes, total_bytes)``
        callback suitable as the ``progress_callback`` parameter for
        :meth:`HttpDownload.download_file`.

        The returned callback is throttled — it only emits a new event when
        the download percentage actually changes, preventing UI flooding.
        After the first second of transfer the message also includes total
        size, current progress, and download speed.

        Args:
            on_progress: Optional callback for :class:`ProgressEvent` objects
                         (e.g. from a Rich spinner).  If ``None``, returns ``None``.

        Returns:
            A ``(current, total) -> None`` download-progress callback, or
            ``None`` when ``on_progress`` is ``None``.

        """
        if on_progress is None:
            return None

        from mvmctl.models.result import ProgressEvent

        fmt = CommonUtils.format_bytes_human_readable
        total_str = ""

        last_pct: list[int] = [0]
        started_at: list[float] = [0.0]

        def _on_download(current: int, total: int | None) -> None:
            nonlocal total_str

            if not total or total <= 0:
                return

            now = time.monotonic()
            if started_at[0] == 0.0:
                started_at[0] = now
                total_str = fmt(total)

            pct = int(100 * current / total)
            if pct == last_pct[0]:
                return
            last_pct[0] = pct

            elapsed = now - started_at[0]
            parts = [f"Downloading... {pct}%  ({fmt(current)}/{total_str})"]

            if elapsed >= 1.0:
                parts.append(f"  ·  {fmt(int(current / elapsed))}/s")

            on_progress(
                ProgressEvent(
                    phase="download",
                    status="running",
                    message="".join(parts),
                )
            )

        return _on_download
