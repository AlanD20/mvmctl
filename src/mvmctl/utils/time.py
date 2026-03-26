"""Time formatting utilities."""

from __future__ import annotations

from datetime import datetime, timezone


def human_readable_time(iso_timestamp: str) -> str:
    """Convert ISO timestamp to human-readable relative time.

    Args:
        iso_timestamp: ISO 8601 formatted timestamp string

    Returns:
        Human-readable string like "5 minutes ago", "2 hours ago", "3 days ago"
    """
    try:
        # Handle both timezone-aware and naive timestamps
        if iso_timestamp.endswith("Z"):
            iso_timestamp = iso_timestamp[:-1] + "+00:00"
        dt = datetime.fromisoformat(iso_timestamp)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        now = datetime.now(tz=timezone.utc)
        delta = now - dt

        seconds = int(delta.total_seconds())
        if seconds < 0:
            return "just now"
        if seconds < 60:
            return "just now"
        if seconds < 3600:
            minutes = seconds // 60
            return f"{minutes} minute{'s' if minutes != 1 else ''} ago"
        if seconds < 86400:
            hours = seconds // 3600
            return f"{hours} hour{'s' if hours != 1 else ''} ago"
        days = seconds // 86400
        return f"{days} day{'s' if days != 1 else ''} ago"
    except (ValueError, TypeError):
        return iso_timestamp  # Return original if parsing fails
