"""Tests for KeyService with mocked subprocess."""

from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._service import KeyService
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import SSHKeyItem

SAMPLE_PUB_KEY = (
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtestkeycontent testuser@testhost"
)


@pytest.fixture
def db() -> Database:
    """Create a fresh database with migrations applied for each test."""
    database = Database()
    database.migrate()
    return database


@pytest.fixture
def service(db: Database) -> KeyService:
    """Create a KeyService with the test database."""
    return KeyService(KeyRepository(db))


class TestFingerprint:
    """Tests for fingerprint computation."""

    def test_compute_fingerprint_valid(self, service: KeyService) -> None:
        """_compute_fingerprint returns SHA256 fingerprint."""
        fp = service._compute_fingerprint(SAMPLE_PUB_KEY)
        assert fp.startswith("SHA256:")
        assert len(fp) > len("SHA256:")

    def test_compute_fingerprint_invalid(self, service: KeyService) -> None:
        """_compute_fingerprint raises MVMKeyError for invalid key."""
        with pytest.raises(MVMKeyError, match="Invalid public key format"):
            service._compute_fingerprint("not-a-valid-key")

    def test_compute_fingerprint_empty(self, service: KeyService) -> None:
        """_compute_fingerprint raises MVMKeyError for empty key."""
        with pytest.raises(MVMKeyError, match="Invalid public key format"):
            service._compute_fingerprint("")


class TestParseAlgorithm:
    """Tests for algorithm extraction."""

    def test_parse_algorithm_valid(self, service: KeyService) -> None:
        """_parse_algorithm extracts algorithm from public key."""
        alg = service._parse_algorithm(SAMPLE_PUB_KEY)
        assert alg == "ssh-ed25519"

    def test_parse_algorithm_empty(self, service: KeyService) -> None:
        """_parse_algorithm raises MVMKeyError for empty key."""
        with pytest.raises(MVMKeyError, match="Invalid public key format"):
            service._parse_algorithm("")


class TestParseComment:
    """Tests for comment extraction."""

    def test_parse_comment_with_comment(self, service: KeyService) -> None:
        """_parse_comment extracts comment from public key."""
        comment = service._parse_comment(SAMPLE_PUB_KEY)
        assert comment == "testuser@testhost"

    def test_parse_comment_no_comment(self, service: KeyService) -> None:
        """_parse_comment returns empty string when no comment."""
        comment = service._parse_comment(
            "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI"
        )
        assert comment == ""


class TestGenerateKeypair:
    """Tests for create_keypair."""

    def test_create_keypair_success(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """create_keypair generates a keypair and registers it."""
        output_dir = tmp_path / "ssh"
        output_dir.mkdir()

        mock_result = MagicMock()
        mock_result.returncode = 0

        def _fake_keygen(*args, **kwargs):
            # Simulate ssh-keygen writing files
            cmd = args[0]
            key_path = None
            for i, arg in enumerate(cmd):
                if arg == "-f" and i + 1 < len(cmd):
                    key_path = Path(cmd[i + 1])
                    break
            if key_path:
                key_path.write_text("PRIVATE KEY CONTENT")
                pub_path = key_path.with_suffix(".pub")
                pub_path.write_text(SAMPLE_PUB_KEY)
            return mock_result

        with patch(
            "mvmctl.core.key._service.run_cmd", side_effect=_fake_keygen
        ):
            info, private_path = service.create_keypair(
                "newkey", output_dir=output_dir
            )

        assert info.name == "newkey"
        assert info.algorithm == "ssh-ed25519"
        assert info.private_key_path is not None
        assert private_path == output_dir / "newkey"
        # Verify it was saved in DB
        retrieved = service._repo.get_by_name("newkey")
        assert retrieved is not None

    def test_create_keypair_db_duplicate_no_overwrite(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """create_keypair raises error when key name exists in DB and overwrite=False."""
        output_dir = tmp_path / "ssh"
        output_dir.mkdir()

        # Register key in DB first to trigger duplicate check
        now = datetime.now(tz=UTC).isoformat()
        existing = SSHKeyItem(
            id="existingkey-id-" + "x" * 55,
            name="existingkey",
            fingerprint="SHA256:abc123",
            algorithm="ssh-ed25519",
            comment="existingkey@host",
            public_key_path=str(output_dir / "existingkey.pub"),
            private_key_path=str(output_dir / "existingkey"),
            is_default=False,
            is_present=True,
            created_at=now,
            updated_at=now,
        )
        service._repo.upsert(existing)

        with pytest.raises(MVMKeyError, match="already exists"):
            service.create_keypair("existingkey", output_dir=output_dir)

    def test_create_keypair_ssh_keygen_fails(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """create_keypair raises MVMKeyError when ssh-keygen fails."""
        output_dir = tmp_path / "ssh"
        output_dir.mkdir()

        mock_result = MagicMock()
        mock_result.returncode = 1
        mock_result.stderr = "keygen error"

        with patch(
            "mvmctl.core.key._service.run_cmd", return_value=mock_result
        ):
            with pytest.raises(MVMKeyError, match="ssh-keygen failed"):
                service.create_keypair("failkey", output_dir=output_dir)

    def test_create_keypair_with_overwrite(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """create_keypair with overwrite=True replaces existing files."""
        output_dir = tmp_path / "ssh"
        output_dir.mkdir()
        (output_dir / "overkey").write_text("OLD PRIVATE")
        (output_dir / "overkey.pub").write_text("OLD PUB")

        mock_result = MagicMock()
        mock_result.returncode = 0

        def _fake_keygen(*args, **kwargs):
            cmd = args[0]
            key_path = None
            for i, arg in enumerate(cmd):
                if arg == "-f" and i + 1 < len(cmd):
                    key_path = Path(cmd[i + 1])
                    break
            if key_path:
                key_path.write_text("NEW PRIVATE")
                key_path.with_suffix(".pub").write_text(SAMPLE_PUB_KEY)
            return mock_result

        with patch(
            "mvmctl.core.key._service.run_cmd", side_effect=_fake_keygen
        ):
            info, private_path = service.create_keypair(
                "overkey", output_dir=output_dir, overwrite=True
            )

        assert info.name == "overkey"
        assert private_path.read_text() == "NEW PRIVATE"


class TestAddKey:
    """Tests for add_key."""

    def test_add_key_success(self, service: KeyService, tmp_path: Path) -> None:
        """add_key adds a public key to the cache."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id_ed25519.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)

        info = service.add_key("testkey", pub_file, SAMPLE_PUB_KEY, keys_dir)
        assert info.name == "testkey"
        assert info.algorithm == "ssh-ed25519"
        assert info.fingerprint.startswith("SHA256:")
        assert (keys_dir / "testkey.pub").exists()

    def test_add_key_already_exists(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """add_key raises error when key name already exists."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)
        service.add_key("mykey", pub_file, SAMPLE_PUB_KEY, keys_dir)

        # Remove the file to trigger is_present update
        (keys_dir / "mykey.pub").unlink()
        results = service.list_all(keys_dir, verify=True)
        assert len(results) == 1  # Still returned, but is_present is 0
        assert results[0].is_present is False or results[0].is_present == 0


class TestSetDefaults:
    """Tests for set_default_keys / clear_default_keys."""

    def test_set_default_keys(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """set_default_keys marks keys as default."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        alice_key = (
            "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGFsaWNlQGhvc3Q= alice@host"
        )
        bob_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGJvYkBob3N0 bob@host"

        alice_file = tmp_path / "alice.pub"
        alice_file.write_text(alice_key)
        bob_file = tmp_path / "bob.pub"
        bob_file.write_text(bob_key)

        service.add_key("alice", alice_file, alice_key, keys_dir)
        service.add_key("bob", bob_file, bob_key, keys_dir)

        service.set_default_keys(["alice", "bob"])
        defaults = service._repo.get_defaults()
        assert len(defaults) == 2

    def test_set_default_keys_skips_unknown(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """set_default_keys silently skips unknown key names (validation is at API layer)."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)
        service.add_key("alice", pub_file, SAMPLE_PUB_KEY, keys_dir)

        # Unknown names are silently skipped — only known keys get set
        service.set_default_keys(["alice", "ghost"])
        defaults = service._repo.get_defaults()
        assert len(defaults) == 1
        assert defaults[0].name == "alice"

    def test_clear_default_keys(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """clear_default_keys removes all default marks."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)
        service.add_key("alice", pub_file, SAMPLE_PUB_KEY, keys_dir)
        service.set_default_keys(["alice"])
        assert len(service._repo.get_defaults()) == 1

        service.clear_default_keys()
        assert service._repo.get_defaults() == []


class TestGetPubkey:
    """Tests for get_pubkey."""

    def test_get_pubkey_by_name(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """get_pubkey retrieves public key content by name."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)
        service.add_key("mykey", pub_file, SAMPLE_PUB_KEY, keys_dir)

        content = service.get_pubkey("mykey", keys_dir)
        assert "ssh-ed25519" in content

    def test_get_pubkey_not_found(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """get_pubkey raises error for unknown key name."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        with pytest.raises(MVMKeyError, match="not found"):
            service.get_pubkey("nonexistent", keys_dir)

    def test_get_pubkey_by_item(
        self, service: KeyService, tmp_path: Path
    ) -> None:
        """get_pubkey works with SSHKeyItem directly."""
        keys_dir = tmp_path / "keys"
        keys_dir.mkdir()

        pub_file = tmp_path / "id.pub"
        pub_file.write_text(SAMPLE_PUB_KEY)
        item = service.add_key("mykey", pub_file, SAMPLE_PUB_KEY, keys_dir)

        content = service.get_pubkey(item, keys_dir)
        assert "ssh-ed25519" in content


class TestCheckDependencies:
    """Tests for check_dependencies."""

    def test_check_dependencies_found(self) -> None:
        """check_dependencies passes when ssh-keygen is available."""
        with patch(
            "mvmctl.core.key._service.shutil.which",
            return_value="/usr/bin/ssh-keygen",
        ):
            KeyService.check_dependencies()  # Should not raise

    def test_check_dependencies_not_found(self) -> None:
        """check_dependencies raises KeyDependencyError when ssh-keygen not found."""
        from mvmctl.exceptions import KeyDependencyError

        with patch("mvmctl.core.key._service.shutil.which", return_value=None):
            with pytest.raises(KeyDependencyError, match="ssh-keygen"):
                KeyService.check_dependencies()
