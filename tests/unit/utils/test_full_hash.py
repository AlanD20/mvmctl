"""Tests for full_hash utility functions."""

from __future__ import annotations

from pathlib import Path

import pytest

from mvmctl.utils.full_hash import (
    generate_full_hash_binary,
    generate_full_hash_image,
    generate_full_hash_kernel,
    generate_full_hash_network,
    generate_full_hash_vm,
    shorten_hash,
)

TIMESTAMP = "2026-04-01T00:00:00.000000"


@pytest.fixture
def sample_file(tmp_path: Path) -> Path:
    """Create a sample file for hash testing."""
    f = tmp_path / "sample.bin"
    f.write_bytes(b"sample content for hashing")
    return f


class TestGenerateFullHashImage:
    def test_returns_64_char_hex(self, sample_file: Path) -> None:
        result = generate_full_hash_image(sample_file, "ubuntu-24.04", TIMESTAMP)
        assert len(result) == 64
        assert all(c in "0123456789abcdef" for c in result)

    def test_deterministic(self, sample_file: Path) -> None:
        h1 = generate_full_hash_image(sample_file, "ubuntu-24.04", TIMESTAMP)
        h2 = generate_full_hash_image(sample_file, "ubuntu-24.04", TIMESTAMP)
        assert h1 == h2

    def test_different_slugs_produce_different_hashes(self, sample_file: Path) -> None:
        h1 = generate_full_hash_image(sample_file, "ubuntu-24.04", TIMESTAMP)
        h2 = generate_full_hash_image(sample_file, "alpine-3.21", TIMESTAMP)
        assert h1 != h2

    def test_different_timestamps_produce_different_hashes(self, sample_file: Path) -> None:
        h1 = generate_full_hash_image(sample_file, "ubuntu-24.04", "2026-01-01T00:00:00")
        h2 = generate_full_hash_image(sample_file, "ubuntu-24.04", "2026-02-01T00:00:00")
        assert h1 != h2

    def test_different_file_contents_produce_different_hashes(self, tmp_path: Path) -> None:
        f1 = tmp_path / "file1.bin"
        f2 = tmp_path / "file2.bin"
        f1.write_bytes(b"content one")
        f2.write_bytes(b"content two")
        h1 = generate_full_hash_image(f1, "ubuntu-24.04", TIMESTAMP)
        h2 = generate_full_hash_image(f2, "ubuntu-24.04", TIMESTAMP)
        assert h1 != h2


class TestGenerateFullHashKernel:
    def test_returns_64_char_hex(self, sample_file: Path) -> None:
        result = generate_full_hash_kernel(sample_file, "6.1.102", "x86_64")
        assert len(result) == 64

    def test_deterministic(self, sample_file: Path) -> None:
        h1 = generate_full_hash_kernel(sample_file, "6.1.102", "x86_64")
        h2 = generate_full_hash_kernel(sample_file, "6.1.102", "x86_64")
        assert h1 == h2

    def test_different_arch_produces_different_hash(self, sample_file: Path) -> None:
        h1 = generate_full_hash_kernel(sample_file, "6.1.102", "x86_64")
        h2 = generate_full_hash_kernel(sample_file, "6.1.102", "aarch64")
        assert h1 != h2


class TestGenerateFullHashBinary:
    def test_returns_64_char_hex(self, sample_file: Path) -> None:
        result = generate_full_hash_binary(sample_file, "firecracker", "1.15.0")
        assert len(result) == 64

    def test_deterministic(self, sample_file: Path) -> None:
        h1 = generate_full_hash_binary(sample_file, "firecracker", "1.15.0")
        h2 = generate_full_hash_binary(sample_file, "firecracker", "1.15.0")
        assert h1 == h2

    def test_different_names_produce_different_hashes(self, sample_file: Path) -> None:
        h1 = generate_full_hash_binary(sample_file, "firecracker", "1.15.0")
        h2 = generate_full_hash_binary(sample_file, "jailer", "1.15.0")
        assert h1 != h2


class TestGenerateFullHashVm:
    def test_returns_64_char_hex(self) -> None:
        result = generate_full_hash_vm("myvm", "a" * 64, "b" * 64, TIMESTAMP)
        assert len(result) == 64

    def test_deterministic(self) -> None:
        h1 = generate_full_hash_vm("myvm", "a" * 64, "b" * 64, TIMESTAMP)
        h2 = generate_full_hash_vm("myvm", "a" * 64, "b" * 64, TIMESTAMP)
        assert h1 == h2

    def test_different_names_produce_different_hashes(self) -> None:
        h1 = generate_full_hash_vm("vm1", "a" * 64, "b" * 64, TIMESTAMP)
        h2 = generate_full_hash_vm("vm2", "a" * 64, "b" * 64, TIMESTAMP)
        assert h1 != h2

    def test_different_image_ids_produce_different_hashes(self) -> None:
        h1 = generate_full_hash_vm("myvm", "a" * 64, "b" * 64, TIMESTAMP)
        h2 = generate_full_hash_vm("myvm", "c" * 64, "b" * 64, TIMESTAMP)
        assert h1 != h2


class TestGenerateFullHashNetwork:
    def test_returns_64_char_hex(self) -> None:
        result = generate_full_hash_network("default", "172.35.0.0/24", TIMESTAMP)
        assert len(result) == 64

    def test_deterministic(self) -> None:
        h1 = generate_full_hash_network("default", "172.35.0.0/24", TIMESTAMP)
        h2 = generate_full_hash_network("default", "172.35.0.0/24", TIMESTAMP)
        assert h1 == h2

    def test_different_subnets_produce_different_hashes(self) -> None:
        h1 = generate_full_hash_network("default", "172.35.0.0/24", TIMESTAMP)
        h2 = generate_full_hash_network("default", "10.0.0.0/24", TIMESTAMP)
        assert h1 != h2


class TestShortenHash:
    def test_returns_12_chars_by_default(self) -> None:
        full = "a" * 64
        assert shorten_hash(full) == "a" * 12

    def test_custom_length(self) -> None:
        full = "b" * 64
        assert shorten_hash(full, length=6) == "b" * 6

    def test_raises_if_hash_shorter_than_length(self) -> None:
        with pytest.raises(ValueError, match="shorter than requested length"):
            shorten_hash("abc", length=12)

    def test_preserves_prefix(self) -> None:
        full = "fbbcdb3b23" + "0" * 54
        assert shorten_hash(full, length=10) == "fbbcdb3b23"
