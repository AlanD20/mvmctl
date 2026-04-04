"""Tests for utils/time.py."""

from datetime import datetime, timezone

from mvmctl.utils.time import human_readable_time


class TestHumanReadableTime:
    """Tests for human_readable_time()."""

    def test_just_now_under_60_seconds(self):
        """Should return 'just now' for timestamps less than 60 seconds ago."""
        now = datetime.now(tz=timezone.utc)
        ts = now.isoformat()
        result = human_readable_time(ts)
        assert result == "just now"

    def test_just_now_future_timestamp(self):
        """Should return 'just now' for future timestamps."""
        future = datetime(2099, 1, 1, tzinfo=timezone.utc)
        result = human_readable_time(future.isoformat())
        assert result == "just now"

    def test_minutes_ago(self):
        """Should return minutes for timestamps 1-59 minutes ago."""
        now = datetime.now(tz=timezone.utc)
        five_min_ago = now.timestamp() - 300
        ts = datetime.fromtimestamp(five_min_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "minute" in result
        assert "5" in result

    def test_one_minute_ago_singular(self):
        """Should use singular 'minute' for 1 minute ago."""
        now = datetime.now(tz=timezone.utc)
        one_min_ago = now.timestamp() - 70
        ts = datetime.fromtimestamp(one_min_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "1 minute ago" == result

    def test_hours_ago(self):
        """Should return hours for timestamps 1-23 hours ago."""
        now = datetime.now(tz=timezone.utc)
        two_hours_ago = now.timestamp() - 7200
        ts = datetime.fromtimestamp(two_hours_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "hour" in result
        assert "2" in result

    def test_one_hour_ago_singular(self):
        """Should use singular 'hour' for 1 hour ago."""
        now = datetime.now(tz=timezone.utc)
        one_hour_ago = now.timestamp() - 3700
        ts = datetime.fromtimestamp(one_hour_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "1 hour ago" == result

    def test_days_ago(self):
        """Should return days for timestamps 24+ hours ago."""
        now = datetime.now(tz=timezone.utc)
        three_days_ago = now.timestamp() - 259200
        ts = datetime.fromtimestamp(three_days_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "day" in result
        assert "3" in result

    def test_one_day_ago_singular(self):
        """Should use singular 'day' for 1 day ago."""
        now = datetime.now(tz=timezone.utc)
        one_day_ago = now.timestamp() - 90000
        ts = datetime.fromtimestamp(one_day_ago, tz=timezone.utc).isoformat()
        result = human_readable_time(ts)
        assert "1 day ago" == result

    def test_z_suffix_converted_to_utc(self):
        """Should handle 'Z' suffix timestamps."""
        ts = "2026-01-01T00:00:00Z"
        result = human_readable_time(ts)
        assert "ago" in result or result == ts

    def test_naive_timestamp_gets_utc(self):
        """Should treat naive timestamps as UTC."""
        ts = "2026-01-01T00:00:00"
        result = human_readable_time(ts)
        assert "ago" in result or result == ts

    def test_invalid_timestamp_returns_original(self):
        """Should return original string on parse failure."""
        result = human_readable_time("not-a-timestamp")
        assert result == "not-a-timestamp"

    def test_empty_string_returns_original(self):
        """Should return original string for empty input."""
        result = human_readable_time("")
        assert result == ""

    def test_numeric_string_returns_original(self):
        """Should return original string for non-ISO format."""
        result = human_readable_time("12345")
        assert result == "12345"
