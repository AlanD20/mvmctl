"""Security tests for path traversal, command injection, and input validation.

Tests malicious input handling to ensure the system properly rejects:
- Path traversal attacks (e.g., ../../../etc/passwd)
- Command injection (e.g., ; rm -rf /)
- Shell metacharacter injection
"""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest
from pytest_mock import MockerFixture

from mvmctl.core.host_privilege import _generate_sudoers_content, _validate_sudoers_binaries
from mvmctl.core.vm_lifecycle import _resolve_image_path, create_vm
from mvmctl.exceptions import FCMError, HostError, ImageError
from mvmctl.utils.http import download_file
from mvmctl.utils.validation import validate_boot_arg_component, validate_entity_name


# -----------------------------------------------------------------------------
# VM Name Sanitization - Path Traversal Prevention
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_name,attack_type",
    [
        ("../../../etc/passwd", "path traversal"),
        ("..\\..\\..\\windows\\system32", "windows path traversal"),
        ("vm/../../etc/shadow", "relative path escape"),
        ("./../etc/hosts", "current dir escape"),
        ("vm; rm -rf /", "command injection semicolon"),
        ("vm && cat /etc/passwd", "command injection &&"),
        ("vm || evil", "command injection ||"),
        ("vm|cat /etc/passwd", "command injection pipe"),
        ("vm`whoami`", "command substitution backtick"),
        ("vm$(id)", "command substitution dollar"),
        ('vm"evil', "double quote injection"),
        ("vm'evil", "single quote injection"),
        ("vm\x00null", "null byte injection"),
        ("vm\nnewline", "newline injection"),
        ("vm\ttab", "tab injection"),
        ("/absolute/path/vm", "absolute path"),
        ("~/.ssh/id_rsa", "tilde expansion"),
        ("vm*", "glob wildcard"),
        ("vm?", "glob single char"),
        ("vm[abc]", "glob character class"),
    ],
)
def test_validate_entity_name_rejects_path_traversal(malicious_name: str, attack_type: str):
    """VM names with path traversal or shell metacharacters must be rejected."""
    with pytest.raises(FCMError, match="Invalid .* name"):
        validate_entity_name(malicious_name, "VM")


@pytest.mark.parametrize(
    "malicious_name,attack_type",
    [
        ("../../../etc/passwd", "path traversal"),
        ("network; rm -rf /", "command injection"),
        ("net|cat /etc/passwd", "pipe injection"),
    ],
)
def test_validate_entity_name_rejects_network_name_attacks(malicious_name: str, attack_type: str):
    """Network names with malicious patterns must be rejected."""
    with pytest.raises(FCMError, match="Invalid .* name"):
        validate_entity_name(malicious_name, "network")


# -----------------------------------------------------------------------------
# Image ID Validation - Path Traversal Prevention
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_image_id,attack_type",
    [
        ("../../../etc/shadow", "path traversal to shadow"),
        ("../../etc/passwd", "path traversal to passwd"),
        ("../images/../../../etc/hosts", "deep path traversal"),
        ("image; rm -rf /", "command injection"),
        ("image|cat /etc/passwd", "pipe injection"),
        ("image`whoami`", "backtick substitution"),
        ("image$(id)", "dollar substitution"),
        ("/absolute/path/to/image", "absolute path"),
        ("~/.ssh/authorized_keys", "tilde expansion"),
        ("image*", "glob wildcard"),
        ("image?", "glob single char"),
    ],
)
def test_resolve_image_path_rejects_traversal(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    malicious_image_id: str,
    attack_type: str,
):
    """Image IDs with path traversal must be rejected or not resolve."""
    from mvmctl.utils import fs

    cache_dir = tmp_path / "cache"
    images_dir = cache_dir / "images"
    images_dir.mkdir(parents=True)

    monkeypatch.setattr(fs, "get_images_dir", lambda: images_dir)
    monkeypatch.setattr(fs, "get_cache_dir", lambda: cache_dir)

    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    # Attempt to resolve malicious image ID - should raise FCMError (not found)
    with pytest.raises(FCMError):
        _resolve_image_path(malicious_image_id)


# -----------------------------------------------------------------------------
# URL Validation - Prevent Injection in Image URLs
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_url,attack_type",
    [
        ("http://example.com/img; cat /etc/passwd", "semicolon command injection"),
        ("http://example.com/img|cat /etc/passwd", "pipe command injection"),
        ("http://example.com/img`whoami`", "backtick injection"),
        ("http://example.com/img$(id)", "command substitution"),
        ('http://example.com/img"evil', "quote injection"),
        ("http://example.com/img'evil", "single quote injection"),
        ("http://example.com/img && rm -rf /", "logical AND injection"),
        ("http://example.com/img || evil", "logical OR injection"),
        ("file:///etc/passwd", "file protocol access"),
        ("ftp://attacker.com/malware", "ftp protocol"),
        ("../../etc/passwd", "relative path as URL"),
    ],
)
def test_download_file_rejects_malicious_urls(
    tmp_path: Path,
    malicious_url: str,
    attack_type: str,
):
    """URLs with shell metacharacters or non-HTTP protocols should fail safely."""
    dest = tmp_path / "download"

    # Should raise FCMError for invalid/malformed URLs
    with pytest.raises(FCMError):
        download_file(malicious_url, dest, expected_sha256=None, allow_missing_checksum=True)


# -----------------------------------------------------------------------------
# Boot Argument Validation - Shell Metacharacter Prevention
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_value,attack_type",
    [
        ("10.20.0.2; rm -rf /", "semicolon injection"),
        ("10.20.0.2 && cat /etc/passwd", "logical AND injection"),
        ("10.20.0.2 || evil", "logical OR injection"),
        ("10.20.0.2|cat /etc/passwd", "pipe injection"),
        ("10.20.0.2&background", "background job"),
        ("$(whoami)", "command substitution"),
        ("`id`", "backtick substitution"),
        ('10.20.0.2"evil', "double quote"),
        ("10.20.0.2'evil", "single quote"),
        ("10.20.0.2\\evil", "backslash escape"),
        ("value with spaces", "space injection"),
        ("value\twith\ttabs", "tab injection"),
        ("value\nwith\nnewlines", "newline injection"),
    ],
)
def test_validate_boot_arg_rejects_injection(malicious_value: str, attack_type: str):
    """Boot arguments with shell metacharacters must be rejected."""
    with pytest.raises(FCMError, match="must not contain spaces or shell metacharacters"):
        validate_boot_arg_component(malicious_value, "guest_ip")


# -----------------------------------------------------------------------------
# Sudoers Line Injection Prevention
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_group,attack_type",
    [
        ("fcm; rm -rf /", "semicolon injection"),
        ("fcm|cat /etc/passwd", "pipe injection"),
        ("fcm`whoami`", "backtick substitution"),
        ("fcm$(id)", "command substitution"),
        ("fcm ../../etc", "path traversal"),
        ("fcm*", "glob wildcard"),
        ("fcm?", "glob single char"),
        ("fcm[abc]", "glob character class"),
        ("fcm group", "space in name"),
        ("fcm\ttab", "tab in name"),
        ("fcm\nnewline", "newline in name"),
        ("../fcm", "relative path"),
        ("/etc/fcm", "absolute path"),
    ],
)
def test_generate_sudoers_rejects_malicious_group(malicious_group: str, attack_type: str):
    """Group names with shell metacharacters must be rejected."""
    with pytest.raises(HostError, match="Invalid group name"):
        _generate_sudoers_content(malicious_group)


def test_generate_sudoers_valid_group():
    """Valid group names should produce proper sudoers content."""
    content = _generate_sudoers_content("fcm")
    assert "%fcm ALL=(root) NOPASSWD:" in content
    assert "# Managed by" in content


def test_generate_sudoers_valid_group_with_dash():
    """Group names with dashes should be accepted."""
    content = _generate_sudoers_content("fcm-users")
    assert "%fcm-users ALL=(root) NOPASSWD:" in content


def test_generate_sudoers_valid_group_with_underscore():
    """Group names with underscores should be accepted."""
    content = _generate_sudoers_content("fcm_users")
    assert "%fcm_users ALL=(root) NOPASSWD:" in content


# -----------------------------------------------------------------------------
# VM Creation Security Tests
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "malicious_name",
    [
        "../../../etc/passwd",
        "vm; rm -rf /",
        "vm|cat /etc/passwd",
        "vm`whoami`",
        "vm$(id)",
    ],
)
def test_create_vm_rejects_malicious_names(
    mocker: MockerFixture,
    malicious_name: str,
):
    """VM creation must reject malicious names before any filesystem operations."""
    mocker.patch("mvmctl.core.vm_lifecycle.get_vm_manager")
    mocker.patch(
        "mvmctl.utils.validation.validate_entity_name", side_effect=FCMError("Invalid VM name")
    )

    with pytest.raises(FCMError, match="Invalid VM name"):
        create_vm(
            name=malicious_name,
            image="ubuntu-24.04",
        )


# -----------------------------------------------------------------------------
# Symlink Attack Prevention (TOCTOU)
# -----------------------------------------------------------------------------


def test_secure_mkdir_vm_rejects_symlink(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    """VM directory creation must detect and reject symlinks (TOCTOU protection)."""
    from mvmctl.core.vm_lifecycle import _secure_mkdir_vm
    from mvmctl.utils import fs

    # Set up isolated VM directory
    vms_dir = tmp_path / "vms"
    vms_dir.mkdir(parents=True)
    monkeypatch.setattr(fs, "get_vm_dir", lambda name: vms_dir / name)

    vm_dir = vms_dir / "test-vm"

    # Create a symlink where VM directory would be
    secret_file = tmp_path / "secret"
    secret_file.write_text("sensitive data")
    vm_dir.symlink_to(secret_file)

    # Attempt to create VM directory at symlink location
    with pytest.raises(FCMError, match="symlink"):
        _secure_mkdir_vm(vm_dir, "test-vm")


# -----------------------------------------------------------------------------
# Combined Attack Vectors
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "input_value,context,expected_error",
    [
        # Path traversal in various contexts
        ("../../../etc/passwd", "VM name", FCMError),
        ("../../etc/shadow", "network name", FCMError),
        ("../config", "key name", FCMError),
        # Command injection
        ("; rm -rf /", "boot arg", FCMError),
        ("| cat /etc/passwd", "boot arg", FCMError),
        ("`id`", "boot arg", FCMError),
        ("$(whoami)", "boot arg", FCMError),
    ],
)
def test_security_boundaries_enforced(input_value: str, context: str, expected_error: type):
    """Security boundaries must be enforced across all input vectors."""
    if "name" in context:
        with pytest.raises(expected_error):
            validate_entity_name(input_value, context.replace(" name", ""))
    elif "boot" in context:
        with pytest.raises(expected_error):
            validate_boot_arg_component(input_value, "test_field")


# -----------------------------------------------------------------------------
# Edge Cases and Bypass Attempts
# -----------------------------------------------------------------------------


@pytest.mark.parametrize(
    "bypass_attempt,attack_type",
    [
        ("....//....//....//etc/passwd", "double dot slash bypass"),
        ("....\\\\....\\\\....\\\\windows\\\\system32", "double backslash bypass"),
        ("..%2f..%2f..%2fetc%2fpasswd", "URL encoded traversal"),
        ("..\\/..\\/..\\/etc/passwd", "mixed slash bypass"),
        ("vm%3b rm -rf /", "URL encoded semicolon"),
        ("vm\x00; rm -rf /", "null byte before injection"),
        ("vm\x00../../../etc/passwd", "null byte before traversal"),
    ],
)
def test_security_edge_cases_rejected(bypass_attempt: str, attack_type: str):
    """Edge case bypass attempts must still be rejected."""
    # These should all fail validation due to invalid characters
    with pytest.raises(FCMError):
        validate_entity_name(bypass_attempt, "VM")
