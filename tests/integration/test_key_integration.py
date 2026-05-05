"""Integration tests for KeyOperation API.

Tests exercise the complete SSH key orchestration flow:
  create → list → get → inspect → set_default → get_defaults → clear_defaults → remove

Only subprocess (ssh-keygen) is mocked. ALL orchestration logic in api/ and core/
runs unmocked.
"""

from __future__ import annotations

import base64
import hashlib
import subprocess
from pathlib import Path

import pytest

from mvmctl.api import KeyCreateInput, KeyInput, KeyOperation
from mvmctl.exceptions import KeyNotFoundError
from mvmctl.models import SSHKeyItem
from mvmctl.models.result import OperationResult
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Helpers
# ======================================================================

_SSH_PUB_ED25519 = (
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHtestkeycontent testuser@testhost"
)


def _compute_expected_fingerprint(pub_content: str) -> str:
    """Compute the SHA256 fingerprint that KeyService will calculate."""
    parts = pub_content.strip().split()
    key_bytes = base64.b64decode(parts[1])
    digest = hashlib.sha256(key_bytes).digest()
    fp = base64.b64encode(digest).rstrip(b"=").decode()
    return f"SHA256:{fp}"


def _setup_ssh_keygen_mock(monkeypatch: pytest.MonkeyPatch) -> None:
    """Patch subprocess.run so that ssh-keygen writes fake key files."""
    from tests.integration.conftest import SmartSubprocessMock

    base = SmartSubprocessMock()

    def _mock_run(
        *args: object, **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        cmd = kwargs.get("args", args[0] if args else [])
        if not isinstance(cmd, list):
            cmd = []

        if cmd and cmd[0] == "ssh-keygen":
            key_path: Path | None = None
            algorithm = "ed25519"
            comment = "testuser@testhost"
            for i, arg in enumerate(cmd):
                if arg == "-f" and i + 1 < len(cmd):
                    key_path = Path(str(cmd[i + 1]))
                if arg == "-t" and i + 1 < len(cmd):
                    algorithm = str(cmd[i + 1])
                if arg == "-C" and i + 1 < len(cmd):
                    comment = str(cmd[i + 1])

            if key_path is not None:
                key_path.parent.mkdir(parents=True, exist_ok=True)
                key_path.write_text(
                    "-----BEGIN OPENSSH PRIVATE KEY-----\n"
                    "fake-private-key-content\n"
                    "-----END OPENSSH PRIVATE KEY-----\n"
                )
                pub_path = key_path.with_suffix(".pub")
                algo_pub = "ssh-rsa" if algorithm == "rsa" else "ssh-ed25519"
                # Unique base64 per key name so fingerprints don't collide
                unique_b64 = base64.b64encode(
                    f"{algo_pub}_{key_path.stem}".encode()
                ).decode()
                pub_path.write_text(f"{algo_pub} {unique_b64} {comment}\n")

            return subprocess.CompletedProcess(
                args=[], returncode=0, stdout="", stderr=""
            )

        return base(*args, **kwargs)

    monkeypatch.setattr("subprocess.run", _mock_run)


# ======================================================================
# TestKeyCreate
# ======================================================================


class TestKeyCreate:
    """Test SSH keypair creation through the real API."""

    def test_create_keypair(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create an ed25519 key and verify DB record + filesystem files."""
        _setup_ssh_keygen_mock(monkeypatch)

        key = KeyOperation.create(KeyCreateInput(name="test-key"))

        assert isinstance(key, OperationResult)
        assert key.status == "success"
        assert isinstance(key.item, SSHKeyItem)
        assert key.item.name == "test-key"
        assert key.item.algorithm == "ssh-ed25519"
        assert key.item.is_present is True
        assert key.item.is_default is False
        import socket

        assert key.item.comment == f"test-key@{socket.gethostname()}"
        keys_dir = CacheUtils.get_keys_dir()
        pub_content = (keys_dir / "test-key.pub").read_text().strip()
        assert key.item.fingerprint == _compute_expected_fingerprint(
            pub_content
        )

        assert (keys_dir / "test-key").exists()
        assert (keys_dir / "test-key.pub").exists()
        assert key.item.public_key_path == str(keys_dir / "test-key.pub")
        assert key.item.private_key_path == str(keys_dir / "test-key")

    def test_create_keypair_rsa(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Create an RSA key and verify the algorithm is parsed correctly."""
        _setup_ssh_keygen_mock(monkeypatch)

        key = KeyOperation.create(
            KeyCreateInput(name="test-rsa", algorithm="rsa", bits=2048)
        )

        assert key.item.algorithm == "ssh-rsa"
        assert key.item.name == "test-rsa"
        keys_dir = CacheUtils.get_keys_dir()
        pub_content = (keys_dir / "test-rsa.pub").read_text().strip()
        assert key.item.fingerprint == _compute_expected_fingerprint(
            pub_content
        )

    def test_create_keypair_default_status(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Creating with set_default=True marks the key as default."""
        _setup_ssh_keygen_mock(monkeypatch)

        key = KeyOperation.create(
            KeyCreateInput(name="test-default", set_default=True)
        )

        assert key.item.is_default is True
        defaults = KeyOperation.get_defaults()
        assert len(defaults) == 1
        assert defaults[0].name == "test-default"

    def test_create_keypair_custom_comment(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Creating with a custom comment stores it correctly."""
        _setup_ssh_keygen_mock(monkeypatch)

        key = KeyOperation.create(
            KeyCreateInput(name="test-comment", comment="my-custom-comment")
        )

        assert key.item.comment == "my-custom-comment"


# ======================================================================
# TestKeyListAndGet
# ======================================================================


class TestKeyListAndGet:
    """Test key listing and retrieval through the real API."""

    def test_list_all_returns_created_keys(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """list_all contains keys created via create()."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(KeyCreateInput(name="list-key"))
        keys = KeyOperation.list_all()
        names = [k.name for k in keys]
        assert "list-key" in names

    def test_get_by_name(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get a key by its exact name."""
        _setup_ssh_keygen_mock(monkeypatch)

        created = KeyOperation.create(KeyCreateInput(name="get-by-name"))
        fetched = KeyOperation.get(KeyInput(name=["get-by-name"]))

        assert fetched.id == created.item.id
        assert fetched.name == "get-by-name"
        assert fetched.algorithm == "ssh-ed25519"

    def test_get_by_id_prefix(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Get a key by the first few characters of its fingerprint ID."""
        _setup_ssh_keygen_mock(monkeypatch)

        created = KeyOperation.create(KeyCreateInput(name="get-by-id"))
        prefix = created.item.id[:6]

        fetched = KeyOperation.get(KeyInput(id=[prefix]))
        assert fetched.id == created.item.id
        assert fetched.name == "get-by-id"

    def test_get_nonexistent_raises_key_not_found(self) -> None:
        """Getting a non-existent key raises KeyNotFoundError."""
        with pytest.raises(KeyNotFoundError):
            KeyOperation.get(KeyInput(name=["no-such-key"]))


# ======================================================================
# TestKeyDefault
# ======================================================================


class TestKeyDefault:
    """Test default key management through the real API."""

    def test_set_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """set_default marks the key as default in the DB."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(KeyCreateInput(name="set-def-key"))
        KeyOperation.set_default(KeyInput(name=["set-def-key"]))

        key = KeyOperation.get(KeyInput(name=["set-def-key"]))
        assert key.is_default

    def test_get_defaults_returns_default_key(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """get_defaults returns only keys marked as default."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(KeyCreateInput(name="def-key-a"))
        KeyOperation.create(KeyCreateInput(name="def-key-b"))
        KeyOperation.set_default(KeyInput(name=["def-key-a"]))

        defaults = KeyOperation.get_defaults()
        assert len(defaults) == 1
        assert defaults[0].name == "def-key-a"
        assert defaults[0].is_default

    def test_clear_defaults_empties_defaults(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """clear_defaults removes the default flag from all keys."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(KeyCreateInput(name="clear-def-key"))
        KeyOperation.set_default(KeyInput(name=["clear-def-key"]))
        assert len(KeyOperation.get_defaults()) == 1

        KeyOperation.clear_defaults()
        assert KeyOperation.get_defaults() == []


# ======================================================================
# TestKeyRemove
# ======================================================================


class TestKeyRemove:
    """Test key removal through the real API."""

    def test_remove_by_name_cleans_db_and_filesystem(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a key deletes its DB record and filesystem files."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(KeyCreateInput(name="remove-key"))
        keys_dir = CacheUtils.get_keys_dir()
        assert (keys_dir / "remove-key.pub").exists()
        assert (keys_dir / "remove-key").exists()

        KeyOperation.remove(KeyInput(name=["remove-key"]))

        assert not (keys_dir / "remove-key.pub").exists()
        assert not (keys_dir / "remove-key").exists()
        with pytest.raises(KeyNotFoundError):
            KeyOperation.get(KeyInput(name=["remove-key"]))

    def test_remove_nonexistent_raises_key_not_found(self) -> None:
        """Removing a non-existent key raises KeyNotFoundError."""
        with pytest.raises(KeyNotFoundError):
            KeyOperation.remove(KeyInput(name=["ghost-key"]))

    def test_remove_default_key_updates_defaults(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a default key removes it from the defaults list."""
        _setup_ssh_keygen_mock(monkeypatch)

        KeyOperation.create(
            KeyCreateInput(name="rm-default-key", set_default=True)
        )
        assert len(KeyOperation.get_defaults()) == 1

        KeyOperation.remove(KeyInput(name=["rm-default-key"]))
        assert KeyOperation.get_defaults() == []


# ======================================================================
# TestKeyEdgeCases
# ======================================================================


class TestKeyEdgeCases:
    """Test edge cases and error handling."""

    def test_add_existing_key_file(self, tmp_path: Path) -> None:
        """Add an existing public key file to the cache."""
        pub_file = tmp_path / "existing_key.pub"
        pub_file.write_text(_SSH_PUB_ED25519 + "\n")

        key = KeyOperation.add(name="added-key", pub_key_path=pub_file)

        assert isinstance(key, OperationResult)
        assert key.status == "success"
        assert isinstance(key.item, SSHKeyItem)
        assert key.item.name == "added-key"
        assert key.item.algorithm == "ssh-ed25519"
        assert key.item.fingerprint == _compute_expected_fingerprint(
            _SSH_PUB_ED25519
        )
        assert key.item.private_key_path is None
        assert key.item.is_default is False

        keys = KeyOperation.list_all()
        names = [k.name for k in keys]
        assert "added-key" in names

    def test_inspect_with_json(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """inspect with is_json=True returns a dict with correct fields."""
        _setup_ssh_keygen_mock(monkeypatch)

        created = KeyOperation.create(KeyCreateInput(name="inspect-json"))
        result = KeyOperation.inspect(
            KeyInput(name=["inspect-json"]), is_json=True
        )

        assert isinstance(result, dict)
        assert result["name"] == "inspect-json"
        assert result["id"] == created.item.id
        assert result["algorithm"] == "ssh-ed25519"
        assert result["fingerprint"] == created.item.fingerprint
        assert not result["is_default"]
        assert "public_key_path" in result
        assert "private_key_path" in result
        assert "created_at" in result

    def test_inspect_nonexistent_raises_key_not_found(self) -> None:
        """Inspecting a non-existent key raises KeyNotFoundError."""
        with pytest.raises(KeyNotFoundError):
            KeyOperation.inspect(KeyInput(name=["no-such-inspect"]))
