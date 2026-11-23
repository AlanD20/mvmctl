from __future__ import annotations

import importlib.util
import os
import stat
import subprocess
import sys
from pathlib import Path
from types import ModuleType

import pytest


@pytest.fixture
def release() -> ModuleType:
    script = Path(__file__).parents[1] / "system_test_release.py"
    module_name = "mvmctl_system_test_release"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


def _stat_result(
    mode: int,
    *,
    uid: int = 0,
    gid: int = 0,
    device: int = 1,
    inode: int = 1,
) -> os.stat_result:
    return os.stat_result((mode, inode, device, 1, uid, gid, 1, 0, 0, 0))


@pytest.mark.parametrize(
    "configured",
    [
        "mvm",
        "/usr/local/bin/mvm --debug",
        "env /usr/local/bin/mvm",
        "/usr/local/bin/../bin/mvm",
        "/tmp/mvm-wrapper",
        "/usr/local/bin/mvm ",
    ],
)
def test_release_controller_rejects_commands_wrappers_and_path_aliases(
    release: ModuleType,
    configured: str,
) -> None:
    with pytest.raises(RuntimeError, match="exactly /usr/local/bin/mvm"):
        release.validate_release_build_paths(
            controller_command=configured,
            configured_candidate=Path("/repo/dist/mvm"),
            candidate=Path("/repo/dist/mvm"),
        )


@pytest.mark.parametrize(
    ("metadata", "controller", "message"),
    [
        (_stat_result(stat.S_IFLNK | 0o777), True, "symlink"),
        (_stat_result(stat.S_IFDIR | 0o755), True, "regular file"),
        (_stat_result(stat.S_IFREG | 0o755, uid=1), True, "root:root"),
        (_stat_result(stat.S_IFREG | 0o755, gid=1), True, "root:root"),
        (_stat_result(stat.S_IFREG | 0o775), True, "mode 0755"),
        (_stat_result(stat.S_IFLNK | 0o777), False, "symlink"),
        (_stat_result(stat.S_IFREG | 0o644), False, "executable"),
    ],
)
def test_release_binary_metadata_is_fail_closed(
    release: ModuleType,
    metadata: os.stat_result,
    controller: bool,
    message: str,
) -> None:
    with pytest.raises(RuntimeError, match=message):
        release._validate_binary_metadata(
            Path("/release/mvm"),
            metadata,
            controller=controller,
        )


def test_release_binary_metadata_accepts_the_required_contract(
    release: ModuleType,
) -> None:
    release._validate_binary_metadata(
        Path("/usr/local/bin/mvm"),
        _stat_result(stat.S_IFREG | 0o755),
        controller=True,
    )
    release._validate_binary_metadata(
        Path("/repo/dist/mvm"),
        _stat_result(stat.S_IFREG | 0o750, uid=1000, gid=1000),
        controller=False,
    )


def test_release_candidate_rejects_a_configured_symlink(
    release: ModuleType,
    tmp_path: Path,
) -> None:
    target = tmp_path / "candidate-target"
    target.write_bytes(b"candidate")
    configured = tmp_path / "candidate"
    configured.symlink_to(target)

    with pytest.raises(RuntimeError, match="candidate path must not be a symlink"):
        release.validate_release_build_paths(
            controller_command="/usr/local/bin/mvm",
            configured_candidate=configured,
            candidate=target,
        )


def test_release_build_paths_reject_the_controller_as_candidate(
    release: ModuleType,
) -> None:
    controller = Path("/usr/local/bin/mvm")

    with pytest.raises(RuntimeError, match="same canonical path"):
        release.validate_release_build_paths(
            controller_command=str(controller),
            configured_candidate=controller,
            candidate=controller,
        )


def test_release_build_paths_reject_an_existing_candidate_inode_alias(
    release: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller"
    candidate = tmp_path / "candidate"
    controller.write_bytes(b"mvm")
    os.link(controller, candidate)
    monkeypatch.setattr(release, "_CANONICAL_CONTROLLER", controller)
    monkeypatch.setattr(
        release,
        "_validate_binary_metadata",
        lambda *_args, **_kwargs: None,
    )
    monkeypatch.setattr(
        release,
        "_validate_controller_ancestors",
        lambda _path: None,
    )

    with pytest.raises(RuntimeError, match="share one inode"):
        release.validate_release_build_paths(
            controller_command=str(controller),
            configured_candidate=candidate,
            candidate=candidate,
        )


def test_release_build_paths_reject_a_symlink_controller_before_build(
    release: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    target = tmp_path / "controller-target"
    target.write_bytes(b"mvm")
    controller = tmp_path / "controller"
    controller.symlink_to(target)
    candidate = tmp_path / "not-built-yet"
    monkeypatch.setattr(release, "_CANONICAL_CONTROLLER", controller)

    with pytest.raises(RuntimeError, match="must not be a symlink"):
        release.validate_release_build_paths(
            controller_command=str(controller),
            configured_candidate=candidate,
            candidate=candidate,
        )


@pytest.mark.parametrize(
    ("metadata", "message"),
    [
        (_stat_result(stat.S_IFLNK | 0o777), "symlink"),
        (_stat_result(stat.S_IFREG | 0o755), "directory"),
        (_stat_result(stat.S_IFDIR | 0o755, uid=1), "root-owned"),
        (_stat_result(stat.S_IFDIR | 0o775), "writable"),
        (_stat_result(stat.S_IFDIR | 0o757), "writable"),
    ],
)
def test_release_controller_ancestors_must_be_trusted(
    release: ModuleType,
    metadata: os.stat_result,
    message: str,
) -> None:
    with pytest.raises(RuntimeError, match=message):
        release._validate_ancestor_metadata(Path("/usr/local/bin"), metadata)


def test_release_binary_evidence_accepts_only_matching_distinct_identity(
    release: ModuleType,
) -> None:
    evidence_type = release._BinaryEvidence
    controller = evidence_type(
        path=Path("/usr/local/bin/mvm"),
        device=1,
        inode=10,
        sha256="a" * 64,
        version="0.3.0-rc.1",
    )
    candidate = evidence_type(
        path=Path("/repo/dist/mvm"),
        device=1,
        inode=11,
        sha256="a" * 64,
        version="0.3.0-rc.1",
    )

    release._validate_matching_evidence(controller, candidate, "0.3.0-rc.1")


@pytest.mark.parametrize(
    ("candidate_changes", "requested_version", "message"),
    [
        ({"path": Path("/usr/local/bin/mvm")}, "0.3.0", "canonical path"),
        ({"device": 1, "inode": 10}, "0.3.0", "inode"),
        ({"sha256": "b" * 64}, "0.3.0", "SHA-256"),
        ({"version": "0.3.1"}, "0.3.0", "versions differ"),
        ({}, "0.3.1", "requested version"),
        ({"version": "v0.3.0"}, "0.3.0", "strict semantic version"),
    ],
)
def test_release_binary_evidence_rejects_alias_or_identity_mismatch(
    release: ModuleType,
    candidate_changes: dict[str, object],
    requested_version: str,
    message: str,
) -> None:
    evidence_type = release._BinaryEvidence
    controller_values: dict[str, object] = {
        "path": Path("/usr/local/bin/mvm"),
        "device": 1,
        "inode": 10,
        "sha256": "a" * 64,
        "version": "0.3.0",
    }
    candidate_values: dict[str, object] = {
        "path": Path("/repo/dist/mvm"),
        "device": 1,
        "inode": 11,
        "sha256": "a" * 64,
        "version": "0.3.0",
    }
    candidate_values.update(candidate_changes)

    with pytest.raises(RuntimeError, match=message):
        release._validate_matching_evidence(
            evidence_type(**controller_values),
            evidence_type(**candidate_values),
            requested_version,
        )


@pytest.mark.parametrize(
    "output",
    [
        "mvm v0.3.0\n",
        "mvm 01.2.3\n",
        "mvm 0.3\n",
        "mvm 0.3.0 extra\n",
        "0.3.0\n",
        "",
    ],
)
def test_release_version_probe_requires_exact_strict_semver_output(
    release: ModuleType,
    output: str,
) -> None:
    with pytest.raises(RuntimeError, match="strict semantic version"):
        release._parse_version_output("candidate", output)


def test_release_version_probe_executes_only_the_pinned_descriptor(
    release: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []
    monkeypatch.setenv("LD_PRELOAD", "/tmp/hostile.so")
    monkeypatch.setenv("MVM_RELEASE_HOSTILE_SENTINEL", "must-not-leak")

    def fake_run(
        command: list[str],
        **kwargs: object,
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(
            command,
            0,
            stdout="mvm 0.3.0-rc.1\n",
            stderr="",
        )

    monkeypatch.setattr(release.subprocess, "run", fake_run)

    version = release._probe_version(73, "controller")

    assert version == "0.3.0-rc.1"
    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command == ["/proc/self/fd/73", "--version"]
    assert kwargs["pass_fds"] == (73,)
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 10
    env = kwargs["env"]
    assert isinstance(env, dict)
    assert env["PATH"] == "/usr/bin:/bin"
    assert env["LANG"] == "C"
    assert env["LC_ALL"] == "C"
    assert "LD_PRELOAD" not in env
    assert "MVM_RELEASE_HOSTILE_SENTINEL" not in env
    for variable in ("MVM_CACHE_DIR", "MVM_CONFIG_DIR", "MVM_TEMP_DIR"):
        assert "mvm-controller-version-" in env[variable]


def test_release_path_recheck_rejects_metadata_downgrade(
    release: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = Path("/usr/local/bin/mvm")
    pinned = release._PinnedBinary(
        path=path,
        descriptor=73,
        metadata=_stat_result(stat.S_IFREG | 0o755, inode=10),
        controller=True,
    )
    monkeypatch.setattr(
        Path,
        "lstat",
        lambda _path: _stat_result(stat.S_IFREG | 0o775, inode=10),
    )

    with pytest.raises(RuntimeError, match="mode 0755"):
        release._require_path_unchanged(pinned)
