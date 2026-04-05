"""Tests for api/init.py."""

from unittest.mock import MagicMock, patch

from mvmctl.api.init import init_database


class TestInitDatabase:
    """Tests for init_database()."""

    def test_creates_db_and_runs_migrations(self):
        """init_database should create MVMDatabase and run migrations."""
        with patch("mvmctl.core.mvm_db.MVMDatabase") as mock_db_class:
            mock_db = MagicMock()
            mock_db_class.return_value = mock_db
            mock_db.migrate.return_value = 2

            init_database()

            mock_db_class.assert_called_once()
            mock_db.migrate.assert_called_once()
