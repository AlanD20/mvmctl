"""Real-filesystem removal boundaries; administrator workflow runs only in disposable QA."""

import json
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace

import pytest


sys.path.insert(0, str(Path(__file__).parents[1]))
import uninstall as subject
import uninstall_files as files


@pytest.fixture
def installation(tmp_path, monkeypatch):
    if os.geteuid() != 0:
        pytest.skip("administrator fixture: run inside the disposable runner as root")
    state = tmp_path / "state"
    host = state / "host"
    host.mkdir(parents=True, mode=0o700)
    home = tmp_path / "home/operator"
    home.mkdir(parents=True)
    uid = 1000
    account = SimpleNamespace(pw_name="operator", pw_uid=uid, pw_gid=uid, pw_dir=str(home))
    os.chown(home, uid, uid)
    monkeypatch.setattr(subject.pwd, "getpwuid", lambda value: account if value == uid else pytest.fail("unexpected owner"))
    journal = {"version": 2, "operator": "operator", "pending": False, "initialized": True}
    (host / "journal.json").write_text(json.dumps(journal))
    (host / "operator-1000.json").write_text(json.dumps({
        "version": 1, "operator": "operator", "uid": "1000", "gid": "1000", "completed": True,
    }))
    (host / "lock").touch(mode=0o600)
    (host / "admission.json").write_text('{"version":1,"enabled":true}')
    binary = tmp_path / "system/mvm"
    binary.parent.mkdir()
    binary.write_text("trusted fixture binary, never executed")
    monkeypatch.setattr(subject, "STATE", state)
    monkeypatch.setattr(subject, "QUARANTINE", tmp_path / "quarantine")
    monkeypatch.setattr(subject, "BINARY", binary)
    monkeypatch.setattr(subject, "RUNTIME", tmp_path / "runtime")
    monkeypatch.setattr(subject, "CGROUP", tmp_path / "cgroup")
    monkeypatch.setattr(subject, "WARM_ROOT", tmp_path / "warm")
    monkeypatch.setattr(subject, "package_removal", lambda: None)
    return state, home, binary


@pytest.mark.parametrize("hazard", ["symlink", "hardlink", "foreign-owner"])
def test_preflight_preserves_entire_tree_on_unsafe_entry(tmp_path, hazard):
    tree = tmp_path / "managed"
    tree.mkdir()
    sentinel = tree / "a-kept"
    sentinel.write_text("must survive a refused removal")
    external = tmp_path / "original"
    external.write_text("original content")
    entry = tree / "z-unsafe"
    if hazard == "symlink":
        entry.symlink_to(external)
    elif hazard == "hardlink":
        os.link(external, entry)
    else:
        if os.geteuid() != 0:
            pytest.skip("ownership fixture needs disposable root")
        entry.write_text("foreign")
        os.chown(entry, 1000, 1000)
    with pytest.raises(files.Refusal):
        files.remove_tree(tree, os.getuid())
    assert sentinel.read_text() == "must survive a refused removal"
    assert external.read_text() == "original content"
    assert os.path.lexists(entry)


def test_intermediate_symlink_never_traversed(tmp_path):
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "keep").write_text("original")
    alias = tmp_path / "alias"
    alias.symlink_to(outside, target_is_directory=True)
    with pytest.raises(OSError):
        files.remove_tree(alias / "keep", os.getuid())
    assert (outside / "keep").read_text() == "original"


def test_capture_does_not_replace_another_generation(tmp_path):
    for name in ("source", "existing"):
        (tmp_path / name).mkdir()
        (tmp_path / name / "proof").write_text(name)
    with files.directory(tmp_path) as parent:
        with pytest.raises(FileExistsError):
            files.capture(parent, "source", "existing")
        files.capture(parent, "source", "captured")
    assert (tmp_path / "existing/proof").read_text() == "existing"
    assert (tmp_path / "captured/proof").read_text() == "source"


def test_personal_launcher_removal_preserves_target_and_original_ssh(tmp_path, monkeypatch):
    system = tmp_path / "trusted-mvm"
    system.write_text("system executable")
    monkeypatch.setattr(subject, "BINARY", system)
    home = tmp_path / "operator"
    launchers = home / ".local/bin"
    launchers.mkdir(parents=True)
    launcher = launchers / "mvm"
    launcher.symlink_to(system)
    original = home / ".ssh/original"
    original.parent.mkdir()
    original.write_text("original key")
    operator = subject.Operator("fixture", os.getuid(), os.getgid(), home)
    subject.personal_binaries(operator, remove=True)
    assert not os.path.lexists(launcher)
    assert system.read_text() == "system executable"
    assert original.read_text() == "original key"


def test_unrecognized_personal_executable_is_preserved_before_cleanup(installation):
    _, home, binary = installation
    parent = home / ".local/bin"
    parent.mkdir(parents=True)
    personal = parent / "mvm"
    personal.write_text("another program with the same name")
    os.chown(personal, 1000, 1000)
    with pytest.raises(files.Refusal, match="unrecognized"):
        subject.make_plan([])
    assert personal.read_text() == "another program with the same name"
    assert binary.exists()


def test_unknown_root_entry_refuses_before_native_cleanup(installation, monkeypatch):
    state, _, binary = installation
    unknown = state / "keep-me"
    unknown.write_text("unowned by mvm")
    monkeypatch.setattr(subject, "native", lambda *args: pytest.fail("must not invoke cleanup"))
    with pytest.raises(files.Refusal, match="unrecognized"):
        subject.make_plan([])
    assert unknown.read_text() == "unowned by mvm"
    assert binary.exists()


def test_retired_cache_locator_reused_for_unrelated_data_is_not_selected(installation):
    state, home, _ = installation
    reused = home / "reused"
    reused.mkdir()
    os.chown(reused, 1000, 1000)
    (reused / "keep").write_text("unrelated data")
    anchor = state / "protected-backing/1000/1-2-3"
    anchor.mkdir(parents=True)
    (anchor / "store.json").write_text(json.dumps({
        "version": 2, "owner": 1000, "locator": str(reused), "retirement": {"phase": "removed"},
    }))
    plan = subject.make_plan([])
    assert plan.operators[1000].caches == {home / ".cache/mvmctl"}
    assert (reused / "keep").read_text() == "unrelated data"


def test_reused_retired_default_cache_refuses_before_deletion(installation):
    state, home, binary = installation
    cache = home / ".cache/mvmctl"
    cache.mkdir(parents=True)
    os.chown(cache, 1000, 1000)
    sentinel = cache / "unrelated"
    sentinel.write_text("keep")
    anchor = state / "protected-backing/1000/1-2-3"
    anchor.mkdir(parents=True)
    (anchor / "store.json").write_text(json.dumps({
        "version": 2, "owner": 1000, "locator": str(cache), "retirement": {"phase": "removed"},
    }))
    with pytest.raises(files.Refusal, match="reuses a retired cache"):
        subject.make_plan([])
    assert sentinel.read_text() == "keep"
    assert binary.exists()


@pytest.mark.parametrize("selection", ["mirror", "ancestor", "overlap"])
def test_unsafe_explicit_selection_is_rejected(installation, selection):
    _, home, _ = installation
    path = {"mirror": home / ".cache/mvm-asset-mirror", "ancestor": home,
            "overlap": home / ".cache/mvmctl/nested"}[selection]
    with pytest.raises(files.Refusal):
        subject.make_plan([f"operator={path}"])


def test_typed_cleanup_failure_preserves_installation_authority(installation, monkeypatch):
    state, home, binary = installation
    cache = home / ".cache/mvmctl"
    cache.mkdir(parents=True)
    os.chown(cache, 1000, 1000)
    (cache / "sentinel").write_text("keep")
    plan = subject.make_plan([])
    def refuse(*args):
        raise files.Refusal("native cleanup refused foreign firewall state")
    monkeypatch.setattr(subject, "native", refuse)
    with pytest.raises(files.Refusal, match="foreign firewall"):
        subject.uninstall(plan)
    assert binary.exists()
    assert (state / "host/journal.json").exists()
    assert (cache / "sentinel").read_text() == "keep"


def test_finalization_requires_disabled_admission(installation):
    state, _, binary = installation
    plan = subject.make_plan([])
    with pytest.raises(files.Refusal, match="reset did not complete"):
        subject.finalize(plan)
    assert binary.exists()
    assert (state / "host/admission.json").read_text() == '{"version":1,"enabled":true}'


def test_completed_reset_allows_final_authority_removal(installation):
    state, _, binary = installation
    host = state / "host"
    (host / "journal.json").write_text(json.dumps({
        "version": 2, "operator": "operator", "pending": False, "initialized": False,
        "reset_state": "complete", "reset_step": 4,
    }))
    (host / "admission.json").write_text('{"version":1,"enabled":false}')
    subject.finalize(subject.make_plan([]))
    assert not state.exists()
    assert not subject.QUARANTINE.exists()
    assert not binary.exists()


def test_dry_run_never_invokes_mvm_or_removes_data(installation, monkeypatch, capsys):
    state, home, binary = installation
    monkeypatch.setattr(sys, "argv", ["uninstall.py", "--dry-run"])
    monkeypatch.setattr(subject, "native", lambda *args: pytest.fail("dry-run invoked mvm"))
    assert subject.main() == 0
    assert "DRY RUN" in capsys.readouterr().out
    assert state.exists() and binary.exists()
    assert not (home / ".cache/mvmctl").exists()


def test_bind_mount_is_not_crossed(tmp_path):
    if os.geteuid() != 0:
        pytest.skip("mount fixture needs disposable root")
    tree, external = tmp_path / "managed", tmp_path / "external"
    tree.mkdir()
    external.mkdir()
    sentinel = external / "original"
    sentinel.write_text("preserved")
    target = tree / "mounted"
    target.mkdir()
    mounted = subprocess.run(["mount", "--bind", str(external), str(target)], capture_output=True)
    if mounted.returncode:
        pytest.skip("runner cannot create bind mount")
    try:
        with pytest.raises(files.Refusal, match="mounted data"):
            files.remove_tree(tree, os.getuid())
        assert sentinel.read_text() == "preserved"
    finally:
        subprocess.run(["umount", str(target)], check=True)
