"""Tests for utils/crypto.py — HashGenerator hashing utilities."""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.utils.crypto import HashGenerator

TIMESTAMP = "2026-04-01T00:00:00.000000"


@pytest.fixture
def sample_file(tmp_path: Path) -> Path:
    """Create a sample file for hash testing."""
    f = tmp_path / "sample.bin"
    f.write_bytes(b"sample content for hashing")
    return f


class TestGenerateHashImage:
    """Tests for HashGenerator.image()."""

    def test_returns_64_char_hex(self):
        result = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", TIMESTAMP
        )
        assert len(result) == 64
        assert all(c in "0123456789abcdef" for c in result)

    def test_deterministic(self):
        h1 = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", TIMESTAMP
        )
        h2 = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", TIMESTAMP
        )
        assert h1 == h2

    def test_different_slugs_produce_different_hashes(self):
        h1 = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", TIMESTAMP
        )
        h2 = HashGenerator.image(
            "alpine-3.21", "https://example.com/img", TIMESTAMP
        )
        assert h1 != h2

    def test_different_timestamps_produce_different_hashes(self):
        h1 = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", "2026-01-01T00:00:00"
        )
        h2 = HashGenerator.image(
            "ubuntu-24.04", "https://example.com/img", "2026-02-01T00:00:00"
        )
        assert h1 != h2


class TestGenerateHashKernel:
    """Tests for HashGenerator.kernel()."""

    def test_returns_64_char_hex(self, sample_file: Path):
        result = HashGenerator.kernel(
            sample_file, "6.1.102", "x86_64", TIMESTAMP
        )
        assert len(result) == 64

    def test_deterministic(self, sample_file: Path):
        h1 = HashGenerator.kernel(sample_file, "6.1.102", "x86_64", TIMESTAMP)
        h2 = HashGenerator.kernel(sample_file, "6.1.102", "x86_64", TIMESTAMP)
        assert h1 == h2

    def test_different_arch_produces_different_hash(self, sample_file: Path):
        h1 = HashGenerator.kernel(sample_file, "6.1.102", "x86_64", TIMESTAMP)
        h2 = HashGenerator.kernel(sample_file, "6.1.102", "aarch64", TIMESTAMP)
        assert h1 != h2


class TestGenerateHashBinary:
    """Tests for HashGenerator.binary()."""

    def test_returns_64_char_hex(self, sample_file: Path):
        result = HashGenerator.binary(sample_file, "firecracker", "1.15.0")
        assert len(result) == 64

    def test_deterministic(self, sample_file: Path):
        h1 = HashGenerator.binary(sample_file, "firecracker", "1.15.0")
        h2 = HashGenerator.binary(sample_file, "firecracker", "1.15.0")
        assert h1 == h2

    def test_different_names_produce_different_hashes(self, sample_file: Path):
        h1 = HashGenerator.binary(sample_file, "firecracker", "1.15.0")
        h2 = HashGenerator.binary(sample_file, "jailer", "1.15.0")
        assert h1 != h2


class TestGenerateHashVm:
    """Tests for HashGenerator.vm()."""

    def test_returns_32_char_hex(self):
        result = HashGenerator.vm("myvm", TIMESTAMP)
        assert len(result) == 32
        assert all(c in "0123456789abcdef" for c in result)

    def test_deterministic(self):
        h1 = HashGenerator.vm("myvm", TIMESTAMP)
        h2 = HashGenerator.vm("myvm", TIMESTAMP)
        assert h1 == h2

    def test_different_names_produce_different_hashes(self):
        h1 = HashGenerator.vm("vm1", TIMESTAMP)
        h2 = HashGenerator.vm("vm2", TIMESTAMP)
        assert h1 != h2


class TestGenerateHashNetwork:
    """Tests for HashGenerator.network()."""

    def test_returns_64_char_hex(self):
        result = HashGenerator.network("default", "172.35.0.0/24", TIMESTAMP)
        assert len(result) == 64

    def test_deterministic(self):
        h1 = HashGenerator.network("default", "172.35.0.0/24", TIMESTAMP)
        h2 = HashGenerator.network("default", "172.35.0.0/24", TIMESTAMP)
        assert h1 == h2

    def test_different_subnets_produce_different_hashes(self):
        h1 = HashGenerator.network("default", "172.35.0.0/24", TIMESTAMP)
        h2 = HashGenerator.network("default", "10.0.0.0/24", TIMESTAMP)
        assert h1 != h2


class TestShortenHash:
    """Tests for HashGenerator.shorten()."""

    def test_returns_12_chars_by_default(self):
        full = "a" * 64
        assert HashGenerator.shorten(full) == "a" * 12

    def test_custom_length(self):
        full = "b" * 64
        assert HashGenerator.shorten(full, length=6) == "b" * 6

    def test_raises_if_hash_shorter_than_length(self):
        with pytest.raises(ValueError, match="shorter than requested length"):
            HashGenerator.shorten("abc", length=12)

    def test_preserves_prefix(self):
        full = "fbbcdb3b23" + "0" * 54
        assert HashGenerator.shorten(full, length=10) == "fbbcdb3b23"
