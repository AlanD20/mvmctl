from __future__ import annotations

import argparse
import ast
import importlib.util
import os
import shutil
import subprocess
import sys
from pathlib import Path
from types import ModuleType

import pytest


@pytest.fixture
def orchestrator() -> ModuleType:
    script = Path(__file__).parents[1] / "run-system-tests.py"
    module_name = "mvmctl_run_system_tests"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


def test_default_outer_controller_is_the_canonical_system_binary(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("MVM_BINARY", raising=False)
    script = Path(__file__).parents[1] / "run-system-tests.py"
    module_name = "mvmctl_run_system_tests_default_controller"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, module_name, module)

    spec.loader.exec_module(module)

    assert module.MVM_BINARY == "/usr/local/bin/mvm"


def test_guest_product_commands_use_the_exact_installed_cli(
    orchestrator: ModuleType,
) -> None:
    tree = ast.parse(Path(orchestrator.__file__).read_text(encoding="utf-8"))
    unqualified = sorted(
        {
            node.value
            for node in ast.walk(tree)
            if isinstance(node, ast.Constant)
            and isinstance(node.value, str)
            and "MVM_ASSET_MIRROR=/mnt mvm " in node.value
        }
    )

    assert unqualified == []


def test_streamed_mvm_failure_preserves_command_and_rc_when_output_is_none(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/outer/controller-mvm")

    def failed_stream(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[None]:
        assert kwargs["capture_output"] is False
        return subprocess.CompletedProcess(command, 23, stdout=None, stderr=None)

    monkeypatch.setattr(orchestrator.subprocess, "run", failed_stream)

    with pytest.raises(RuntimeError) as captured:
        orchestrator.mvm(
            "image",
            "pull",
            "ubuntu-minimal:24.04",
            capture=False,
        )

    message = str(captured.value)
    assert (
        "mvm command failed: /outer/controller-mvm image pull "
        "ubuntu-minimal:24.04"
    ) in message
    assert "rc: 23" in message
    assert "stderr: (streamed; not captured)" in message


def test_builder_assets_are_pulled_in_separate_sequential_execs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets("base-img-builder")

    expected_pull_commands = [
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm binary pull firecracker "
        "--default --force --version 1.16.0",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm kernel pull "
        "--type firecracker --version v1.15 --default",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull alpine:3.23",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull "
        "ubuntu-minimal:24.04",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull ubuntu "
        "--version 24.04",
    ]
    pull_calls = calls[:-1]
    assert [args[-1] for args, _kwargs in pull_calls] == expected_pull_commands
    assert len(pull_calls) == 5
    for args, kwargs in pull_calls:
        assert args[:-1] == (
            "exec",
            "base-img-builder",
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
        )
        assert " & " not in args[-1]
        assert "pids=" not in args[-1]
        assert kwargs["capture"] is False
        assert kwargs["timeout"] == 600

    verify_args, verify_kwargs = calls[-1]
    assert verify_args[:2] == ("exec", "base-img-builder")
    assert "Verifying cached image integrity" in verify_args[-1]
    assert verify_kwargs == {"timeout": 180, "capture": False}


def test_builder_asset_pull_preserves_first_streamed_failure(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    commands: list[str] = []
    original_failure = RuntimeError(
        "mvm command failed: streamed ubuntu-minimal pull; rc: 23; "
        "stderr: (streamed; not captured)"
    )

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        commands.append(args[-1])
        if "ubuntu-minimal:24.04" in args[-1]:
            raise original_failure
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._pull_builder_assets("base-img-builder")

    assert captured.value is original_failure
    assert commands[-1].endswith("image pull ubuntu-minimal:24.04")
    assert not any("--version 24.04" in command for command in commands)
    assert not any("Verifying cached image integrity" in command for command in commands)


def test_build_base_image_cleans_builder_when_asset_pull_fails(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "mvm-candidate"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        stdout = ""
        if args[:2] == ("exec", orchestrator.BASE_VM_NAME) and args[-1].startswith("stat -c%s"):
            size = candidate.stat().st_size
            stdout = f"{size}\n{size}\n"
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    original_failure = RuntimeError("asset pull failed")

    def fail_asset_pull(_vm_name: str) -> None:
        raise original_failure

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_install_system_binary_in_runner", lambda _vm_name: None
    )
    monkeypatch.setattr(
        orchestrator, "_initialize_system_binary_in_runner", lambda _vm_name: None
    )
    monkeypatch.setattr(orchestrator, "_pull_builder_assets", fail_asset_pull)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._build_base_image("0.3.0-test", rebuild=True)

    assert captured.value is original_failure
    builder_removals = [
        args
        for args in calls
        if args == ("vm", "rm", orchestrator.BASE_VM_NAME, "--force")
    ]
    assert len(builder_removals) == 2
    assert not any(args[:2] == ("image", "import") for args in calls)


@pytest.mark.parametrize("backing_kind", ["missing", "directory", "symlink"])
def test_ensure_shared_volume_rebuilds_stale_volume_records(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    backing_kind: str,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if backing_kind == "directory":
        backing.mkdir()
    elif backing_kind == "symlink":
        target = tmp_path / "target.raw"
        target.write_bytes(b"volume")
        backing.symlink_to(target)

    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(backing)),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    seeded: list[bool] = []
    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_create_and_seed_volume", lambda: seeded.append(True)
    )

    orchestrator.ensure_shared_volume()

    assert calls == [
        (
            ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
            {"timeout": 30},
        )
    ]
    assert seeded == [True]


def test_ensure_shared_volume_rejects_poisoned_arbitrary_path_without_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(unrelated)),
    )

    def unexpected_mvm(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("poisoned records must not trigger volume mutation")

    monkeypatch.setattr(orchestrator, "mvm", unexpected_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator.ensure_shared_volume()

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_uses_unprivileged_mkfs_directory_copy(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text("{}", encoding="utf-8")
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"\0" * 4096)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command[:4] == ["mkfs.ext4", "-F", "-d", str(mirror)]
    assert command[4].startswith("/proc/self/fd/")
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 900
    assert kwargs["pass_fds"] == (int(command[4].rsplit("/", 1)[-1]),)
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_populate_volume_image_rejects_symlink_backing_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    target = tmp_path / "target.raw"
    target.write_bytes(b"volume")
    backing.symlink_to(target)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for a symlink backing path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._populate_volume_image(backing, mirror)


def test_populate_volume_image_rejects_symlinked_cache_ancestor_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    actual_cache = tmp_path / "actual-cache"
    actual_backing = actual_cache / "volumes" / "asset-mirror.raw"
    actual_backing.parent.mkdir(parents=True)
    actual_backing.write_bytes(b"volume")
    configured_cache = tmp_path / "configured-cache"
    configured_cache.symlink_to(actual_cache, target_is_directory=True)
    recorded_backing = configured_cache / "volumes" / "asset-mirror.raw"
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(configured_cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not traverse a symlinked cache ancestor")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="safely open shared volume directory"):
        orchestrator._populate_volume_image(recorded_backing, mirror)

    assert actual_backing.read_bytes() == b"volume"


def test_create_volume_removes_record_when_created_backing_is_missing(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    missing_backing = cache / "volumes" / "asset-mirror.raw"
    missing_backing.parent.mkdir(parents=True)
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(missing_backing)),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="backing file is missing"):
        orchestrator._create_and_seed_volume()

    assert calls[0][0][:2] == ("volume", "create")
    assert calls[-1] == (
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
        {"check": False, "timeout": 30},
    )


@pytest.mark.parametrize("leftover_kind", ["dangling_symlink", "directory", "file"])
def test_stale_record_cleanup_must_remove_backing_path_before_volume_create(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    leftover_kind: str,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if leftover_kind == "dangling_symlink":
        backing.symlink_to(tmp_path / "missing-target.raw")
    elif leftover_kind == "directory":
        backing.mkdir()
    else:
        backing.write_bytes(b"volume")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(backing)),
    )
    calls: list[tuple[str, ...]] = []

    def stale_remove(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[:2] == ("volume", "create"):
            raise AssertionError("volume create must not follow stale cleanup")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", stale_remove)

    with pytest.raises(RuntimeError, match="still exists before volume create"):
        orchestrator.ensure_shared_volume(rebuild=True)

    assert calls == [
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force")
    ]
    assert backing.lstat()


def test_created_volume_with_unsafe_record_path_skips_automatic_cleanup(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(unrelated)),
    )
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._create_and_seed_volume()

    assert len(calls) == 1
    assert calls[0][:2] == ("volume", "create")
    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_rejects_arbitrary_regular_file(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for an arbitrary path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._populate_volume_image(unrelated, mirror)

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_detects_path_replacement_and_pins_original_inode(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"volume")
    original_inode = backing.stat().st_ino
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    victim = tmp_path / "victim.data"
    victim.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    command_seen: list[str] = []

    def replace_path_during_mkfs(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        command_seen.extend(command)
        backing.unlink()
        backing.symlink_to(victim)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(
        orchestrator.subprocess, "run", replace_path_during_mkfs
    )

    with pytest.raises(RuntimeError, match="replaced during population"):
        orchestrator._populate_volume_image(backing, mirror)

    pinned_fd = int(command_seen[-1].rsplit("/", 1)[-1])
    assert command_seen[-1] == f"/proc/self/fd/{pinned_fd}"
    assert original_inode != victim.stat().st_ino
    assert victim.read_bytes() == b"must survive"


@pytest.mark.skipif(
    shutil.which("mkfs.ext4") is None or shutil.which("debugfs") is None,
    reason="e2fsprogs is required for the direct ext4 population check",
)
def test_populate_volume_image_directly_seeds_a_real_ext4_image(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    with backing.open("wb") as volume_file:
        volume_file.truncate(32 * 1024 * 1024)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text('{"seeded": true}\n', encoding="utf-8")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    inspected = subprocess.run(
        ["debugfs", "-R", "cat /manifest.json", str(backing)],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert inspected.returncode == 0, inspected.stderr
    assert inspected.stdout == '{"seeded": true}\n'
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_candidate_version_probe_is_exact_and_isolated_from_host_state(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "candidate-mvm"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/host/controller-mvm")
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(
            command, 0, stdout="mvm 0.3.0-rc.1\n", stderr=""
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    version = orchestrator._get_mvm_version()

    assert version == "0.3.0-rc.1"
    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command == [str(candidate), "--version"]
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 10
    env = kwargs["env"]
    assert isinstance(env, dict)
    for variable in ("MVM_CACHE_DIR", "MVM_CONFIG_DIR", "MVM_TEMP_DIR"):
        assert env[variable] != os.environ.get(variable)
        assert "mvm-candidate-version-" in env[variable]


def test_rebuild_outputs_only_to_the_configured_candidate(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[list[str]] = []

    def fake_run(command: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(command)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setattr(
        orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.1"
    )

    orchestrator._build_mvm_binary("0.3.0-rc.1")

    assert len(calls) == 1
    assert calls[0][-4:-2] == ["--version", "0.3.0-rc.1"]
    assert calls[0][-2:] == ["--output", str(candidate)]
    assert str(controller) not in calls[0]


def test_rebuild_rejects_candidate_with_wrong_reported_version(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout="", stderr=""
        ),
    )
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.2.6")

    with pytest.raises(RuntimeError, match="requested 0.3.0, reported 0.2.6"):
        orchestrator._build_mvm_binary("0.3.0")


def test_explicit_candidate_version_does_not_consult_git(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("an explicit candidate version must not consult git")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    assert orchestrator._resolve_candidate_build_version("v0.3.0-rc.2") == (
        "0.3.0-rc.2"
    )


def test_candidate_version_uses_one_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        stdout = "v0.3.0-rc.3\n" if command[1] == "tag" else ""
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=stdout,
            stderr="",
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    assert orchestrator._resolve_candidate_build_version(None) == "0.3.0-rc.3"
    assert len(calls) == 2
    assert calls[0][0] == [
        "git",
        "tag",
        "--points-at",
        "HEAD",
        "--list",
        "v[0-9]*",
    ]
    assert calls[1][0] == [
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
    ]
    for _command, kwargs in calls:
        assert kwargs["cwd"] == str(orchestrator._REPO_ROOT)
        assert kwargs["capture_output"] is True
        assert kwargs["text"] is True
        assert kwargs["timeout"] == 10


@pytest.mark.parametrize("tag_output", ["", "v0.2.0\nv0.3.0\n", "release-0.3.0\n"])
def test_candidate_version_rejects_untagged_or_ambiguous_checkout(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tag_output: str,
) -> None:
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout=tag_output, stderr=""
        ),
    )

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_rejects_dirty_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def fake_run(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        stdout = "v0.3.0\n" if command[1] == "tag" else " M internal/file.go\n"
        return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_flag_is_typed(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    parsed = parser.parse_args(
        ["--rebuild", "--candidate-version", "v0.3.0-rc.4"]
    )
    assert parsed.candidate_version == "0.3.0-rc.4"

    with pytest.raises(SystemExit):
        parser.parse_args(["--rebuild", "--candidate-version", "not-a-version"])


@pytest.mark.parametrize(
    "version",
    [
        "01.2.3",
        "1.02.3",
        "1.2.03",
        "1.2.3-01",
        "1.2.3-",
        "1.2.3-rc..1",
        "1.2.3-rc.",
        "1.2.3+",
        "1.2.3+meta..build",
        "1.2.3+meta.",
    ],
)
def test_candidate_version_parser_rejects_non_semver_identifiers(
    orchestrator: ModuleType,
    version: str,
) -> None:
    with pytest.raises(argparse.ArgumentTypeError):
        orchestrator._parse_candidate_version(version)


def test_candidate_version_flag_requires_rebuild(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", "--candidate-version", "0.3.0"],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2


def test_rebuild_rejects_candidate_controller_alias_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(controller))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not start when candidate aliases controller")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_symlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    target = tmp_path / "candidate-target"
    target.write_bytes(b"candidate")
    configured = tmp_path / "candidate-mvm"
    configured.symlink_to(target)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", configured)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(target))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not follow a candidate symlink")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_controller_hardlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "candidate-mvm"
    os.link(controller, candidate)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not replace a controller inode alias")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")
