"""Tests for utils/operation_utils.py — download progress bridge.

This is a throttled callback factory that maps raw HTTP download progress
(int, int | None) onto ProgressEvent emissions for the UI layer.
"""

from __future__ import annotations

from mvmctl.models.result import ProgressEvent
from mvmctl.utils.operation_utils import OperationUtils


class TestDownloadProgressBridge:
    """Tests for OperationUtils.download_progress_bridge().

    The bridge has three key behaviours we test:

    1.  Early-exit — when ``on_progress`` is ``None`` the bridge itself
        returns ``None``; when ``total`` is falsy (``None`` or ``0``) the
        inner callback returns immediately without emitting.
    2.  Throttling — the inner callback only emits a ``ProgressEvent`` when
        the download percentage actually changes; identical consecutive
        percentages are suppressed.
    3.  Speed decoration — after one second of wall-clock time has elapsed
        since the **first** call, the message string grows a ``  ·  X/s``
        suffix showing the average download rate.
    """

    # ------------------------------------------------------------------
    # Early-exit / guard clauses
    # ------------------------------------------------------------------

    def test_no_progress_callback_returns_none(self) -> None:
        """When on_progress is None, the bridge returns None."""
        bridge = OperationUtils.download_progress_bridge(on_progress=None)
        assert bridge is None

    def test_zero_total_does_not_emit(self) -> None:
        """total=0 causes the inner callback to return immediately."""
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None
        bridge(50, 0)
        assert len(events) == 0

    def test_none_total_does_not_emit(self) -> None:
        """total=None causes the inner callback to return immediately."""
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None
        bridge(50, None)
        assert len(events) == 0

    # ------------------------------------------------------------------
    # Throttling — same percentage should not produce duplicate events
    # ------------------------------------------------------------------

    def test_same_percent_skips_duplicate(self) -> None:
        """Repeated calls with the same percentage produce one event."""
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None

        # Call with (1, 200) → int(100 * 1 / 200) = 0
        # This matches initial last_pct=[0], so it is throttled.
        bridge(1, 200)
        assert len(events) == 0, "pct=0 is initial value → throttled"

        # (2, 200) → 1 % → first real change → emit
        bridge(2, 200)
        assert len(events) == 1

        # (3, 200) → 1 % again → throttled
        bridge(3, 200)
        assert len(events) == 1, "pct=1 unchanged → throttled"

    # ------------------------------------------------------------------
    # Event structure and message content
    # ------------------------------------------------------------------

    def test_emits_progress_event_with_correct_fields(self) -> None:
        """Emitted ProgressEvent has the expected phase, status and message."""
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None
        bridge(50, 100)

        assert len(events) == 1
        ev = events[0]
        assert isinstance(ev, ProgressEvent)
        assert ev.phase == "download"
        assert ev.status == "running"
        assert "50%" in ev.message
        # Size formatting: 50 bytes is "50 B"
        assert "50 B" in ev.message or "50" in ev.message

    def test_message_contains_formatted_current_and_total(self) -> None:
        """Message includes the human-readable current/total sizes.

        CommonUtils.format_bytes_human_readable produces IEC binary units,
        so 1024 bytes → "1.0 KiB", 1048576 → "1.0 MiB", etc.
        """
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None

        # 1 MiB out of 2 MiB → 50 %
        bridge(1_048_576, 2_097_152)
        assert len(events) == 1
        msg = events[0].message
        # Should contain "1.0 MiB" and "2.0 MiB"
        assert "MiB" in msg
        assert "50%" in msg

    # ------------------------------------------------------------------
    # Sequential progress through different percentages
    # ------------------------------------------------------------------

    def test_emits_on_each_new_percentage(self) -> None:
        """Each distinct percentage change produces one event.

        This simultaneously validates the *unthrottle* path — when the
        percentage *does* change, ``last_pct`` is updated and a new
        ``ProgressEvent`` is fired.
        """
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None

        # Range 0..100 in steps of 10 → 11 distinct raw values.
        # pct = int(100 * current / total):
        #   current=0  → pct=0 → throttled (initial value)
        #   current=10 → pct=10 → emit  (1)
        #   current=20 → pct=20 → emit  (2)
        #   ...
        #   current=100 → pct=100 → emit (10)
        for current in range(0, 101, 10):
            bridge(current, 100)

        assert len(events) == 10

    # ------------------------------------------------------------------
    # Speed decoration — only shown after ≥ 1 second of wall-clock time
    # ------------------------------------------------------------------

    def test_speed_displayed_after_one_second(self, mocker) -> None:
        """After elapsed ≥ 1.0 s the message includes a speed suffix."""
        import time as real_time

        # The speed suffix only appears on the *second* call because
        # ``started_at`` is set during the first call.
        # First call:  time.monotonic → 10.0  (sets started_at)
        # Second call: time.monotonic → 13.0  (elapsed = 3.0 ≥ 1.0)
        mocker.patch.object(real_time, "monotonic", side_effect=[10.0, 13.0])

        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None

        bridge(10, 100)  # started_at=10.0, elapsed=0.0 → no speed
        bridge(50, 100)  # now=13.0, elapsed=3.0 ≥ 1.0 → speed suffix

        assert len(events) == 2
        msg = events[1].message
        assert "·" in msg or "/s" in msg
        # The speed should be ≈ fmt(int(50/3)) = approx 16 B/s
        assert "B/s" in msg

    def test_no_speed_before_one_second(self, mocker) -> None:
        """Before 1 s has elapsed the message does NOT include speed."""
        import time as real_time

        # First call:  time.monotonic → 10.0  (sets started_at)
        # Second call: time.monotonic → 10.3  (elapsed = 0.3 < 1.0)
        mocker.patch.object(real_time, "monotonic", side_effect=[10.0, 10.3])

        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None

        bridge(10, 100)  # started_at=10.0, elapsed=0.0 → no speed
        bridge(20, 100)  # now=10.3, elapsed=0.3 < 1.0 → no speed

        assert len(events) == 2
        msg = events[1].message
        assert "·" not in msg
        assert "/s" not in msg

    # ------------------------------------------------------------------
    # Edge cases
    # ------------------------------------------------------------------

    def test_handles_small_download(self) -> None:
        """Very small downloads (1/2 bytes) work without division errors."""
        events: list[ProgressEvent] = []
        bridge = OperationUtils.download_progress_bridge(
            on_progress=events.append
        )
        assert bridge is not None
        bridge(1, 2)
        assert len(events) == 1
        assert "50%" in events[0].message
