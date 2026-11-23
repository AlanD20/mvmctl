"""Worker fixture admission and reversible file publication; no privileged effects."""

from copy import deepcopy
import json
from pathlib import Path
import re
from types import SimpleNamespace

import pytest

from scripts.qualification import worker_observer_fixture as tool
from tests.system.network import floating_ip_worker_fault as observer


@pytest.fixture
def binding():
    source = b"# exact reviewed self-contained observer\n"
    payload = tool.PREFIX + source
    manifest = dict(schema_version=1, hostname="mvmctl-030-test", boot_id="11111111-1111-4111-8111-111111111111",
                    operator=dict(user="runner", uid=1001, gid=1002, home="/home/runner"),
                    source_sha256=tool.digest(source), payload_sha256=tool.digest(payload),
                    tool_sha256="a" * 64, enrollment_sha256="b" * 64, policy_sha256="c" * 64)
    return manifest, payload, source


def test_exact_fixture_provenance_is_admitted(binding):
    manifest, payload, source = binding
    assert observer._validate_fixture_values(manifest, payload, source, 1001, 1002,
                                             manifest["hostname"], manifest["boot_id"]) is manifest


@pytest.mark.parametrize("fault", ["unknown", "schema", "host", "boot", "uid", "gid", "boolean_uid", "operator_extra",
                                  "payload", "source", "unprefixed", "tool_hash", "policy_hash"])
def test_fixture_refuses_rebound_scope_or_source(binding, fault):
    manifest, payload, source = deepcopy(binding)
    if fault == "unknown":manifest["argv"] = ["arbitrary"]
    elif fault == "schema":manifest["schema_version"] = True
    elif fault == "host":manifest["hostname"] = "workstation"
    elif fault == "boot":manifest["boot_id"] = "other-boot"
    elif fault == "uid":manifest["operator"]["uid"] = 1003
    elif fault == "gid":manifest["operator"]["gid"] = 1003
    elif fault == "boolean_uid":manifest["operator"]["uid"] = True
    elif fault == "operator_extra":manifest["operator"]["command"] = "python"
    elif fault == "payload":payload += b"# changed\n"
    elif fault == "source":source += b"# changed\n"
    elif fault == "unprefixed":payload = source
    elif fault == "tool_hash":manifest["tool_sha256"] = "not-a-hash"
    else:manifest["policy_sha256"] = "C" * 64
    with pytest.raises(AssertionError):
        observer._validate_fixture_values(manifest, payload, source, 1001, 1002,
                                          "mvmctl-030-test", binding[0]["boot_id"])


@pytest.mark.parametrize("arguments,accepted", [
    ("a" * 32 + " 1001 1002", True), ("a" * 31 + " 1001 1002", False),
    ("A" * 32 + " 1001 1002", False), ("a" * 32 + " 0 1002", False),
    ("a" * 32 + " 1001 0", False), ("a" * 32 + " 1001 1002 extra", False),
    ("a" * 32 + " 01001 1002", False), ("a" * 32 + "; /bin/sh 1001 1002", False),
])
def test_sudo_policy_argument_regex_admits_only_exact_principal(arguments, accepted):
    policy = tool.policy_bytes(1001, 1002).decode()
    prefix = f"#1001 ALL=(root) NOPASSWD: {tool.EXECUTABLE} "
    assert policy.startswith(prefix)
    expression = policy[len(prefix):].rstrip("\n")
    assert (re.fullmatch(expression, arguments) is not None) is accepted


def test_read_refuses_symlink_source_without_following_it(tmp_path):
    original = tmp_path / "observer.py"
    original.write_bytes(b"unchanged")
    link = tmp_path / "alias.py"
    link.symlink_to(original)
    with pytest.raises(OSError):tool.read(link)
    assert original.read_bytes() == b"unchanged"


@pytest.fixture
def installation(monkeypatch, tmp_path):
    # Redirect every administrative path to private test scratch and stub visudo.
    # Parent ownership is tested by the admission readers, not granted on this host.
    executable = tmp_path / "libexec" / "observer"
    executable.parent.mkdir()
    state = tmp_path / "state"
    policy = tmp_path / "sudoers" / "qa-worker"
    policy.parent.mkdir()
    enrollment = policy.parent / "mvm"
    enrollment.write_bytes(b"original normal enrollment\n")
    enrollment.chmod(0o440)
    for key, value in dict(EXECUTABLE=executable, STATE=state, MANIFEST=state / "manifest.json",
                           POLICY=policy, ENROLLMENT=enrollment).items():
        monkeypatch.setattr(tool, key, value)
    original_read = tool.read
    monkeypatch.setattr(tool, "directory", lambda path, **kwargs: Path(path).stat())
    monkeypatch.setattr(tool, "read", lambda path, **kwargs: original_read(
        path, root_owned=False, mode=kwargs.get("mode"), links=kwargs.get("links", (1,))))
    monkeypatch.setattr(tool, "visudo", lambda path=None: None)
    source = tmp_path / "source.py"
    source.write_bytes(b"# reviewed self-contained observer\n")
    args = SimpleNamespace(source=source, source_sha256=tool.digest(source.read_bytes()), tool_sha256="a" * 64,
                           boot_id="11111111-1111-4111-8111-111111111111")
    operator = dict(user="runner", uid=1001, gid=1002, home="/home/runner")
    return args, operator, enrollment.read_bytes()


def test_install_and_remove_preserve_normal_enrollment(installation):
    args, operator, enrollment = installation
    manifest = tool.install(args, operator, enrollment)
    assert tool.EXECUTABLE.read_bytes() == tool.PREFIX + args.source.read_bytes()
    assert tool.POLICY.read_bytes() == tool.policy_bytes(1001, 1002)
    assert json.loads(tool.MANIFEST.read_bytes()) == manifest
    assert tool.ENROLLMENT.read_bytes() == enrollment
    assert tool.uninstall(args, operator, enrollment)["removed"] is True
    assert not any(path.exists() for path in (tool.STATE, tool.POLICY, tool.EXECUTABLE))
    assert tool.ENROLLMENT.read_bytes() == enrollment


@pytest.mark.parametrize("phase", ["staged", "published"])
def test_failed_visudo_rolls_back_only_created_fixture_files(installation, monkeypatch, phase):
    args, operator, enrollment = installation
    calls = []
    def fail(path=None):
        calls.append(path)
        if (path is None) == (phase == "published"):
            raise RuntimeError("visudo rejected exact qualification policy")
    monkeypatch.setattr(tool, "visudo", fail)
    with pytest.raises(RuntimeError, match="visudo rejected"):
        tool.install(args, operator, enrollment)
    assert not any(path.exists() for path in (tool.STATE, tool.POLICY, tool.EXECUTABLE))
    assert tool.ENROLLMENT.read_bytes() == enrollment
    assert calls[0] == tool.STATE / "sudoers.pending"


@pytest.mark.parametrize("foreign", ["executable", "policy", "state"])
def test_existing_foreign_fixture_is_refused_without_mutation(installation, foreign):
    args, operator, enrollment = installation
    path = {"executable": tool.EXECUTABLE, "policy": tool.POLICY, "state": tool.STATE}[foreign]
    if foreign == "state":path.mkdir()
    else:path.write_bytes(b"foreign resource")
    with pytest.raises(ValueError, match="existing/foreign"):
        tool.install(args, operator, enrollment)
    assert path.is_dir() if foreign == "state" else path.read_bytes() == b"foreign resource"
    assert tool.ENROLLMENT.read_bytes() == enrollment


def test_source_mismatch_fails_before_any_fixture_publication(installation):
    args, operator, enrollment = installation
    args.source_sha256 = "0" * 64
    with pytest.raises(ValueError, match="source SHA256"):
        tool.install(args, operator, enrollment)
    assert not any(path.exists() for path in (tool.STATE, tool.POLICY, tool.EXECUTABLE))


@pytest.mark.parametrize("fault", ["source", "payload", "policy", "manifest", "foreign_state"])
def test_remove_refuses_changed_fixture_and_keeps_admission_untouched(installation, fault):
    args, operator, enrollment = installation
    tool.install(args, operator, enrollment)
    if fault == "source":args.source_sha256 = "0" * 64
    elif fault == "payload":
        tool.EXECUTABLE.chmod(0o755)
        tool.EXECUTABLE.write_bytes(b"foreign payload")
        tool.EXECUTABLE.chmod(0o555)
    elif fault == "policy":
        tool.POLICY.chmod(0o640)
        tool.POLICY.write_bytes(b"foreign policy")
        tool.POLICY.chmod(0o440)
    elif fault == "manifest":
        manifest = json.loads(tool.MANIFEST.read_bytes())
        manifest["operator"]["uid"] = 1003
        tool.MANIFEST.write_text(json.dumps(manifest))
    else:(tool.STATE / "foreign").write_bytes(b"untouched")
    original_policy = tool.POLICY.read_bytes()
    with pytest.raises(ValueError):tool.uninstall(args, operator, enrollment)
    assert tool.POLICY.read_bytes() == original_policy
    assert tool.ENROLLMENT.read_bytes() == enrollment


@pytest.mark.parametrize("fault", [None, "root", "host", "boot", "account", "reverse", "home", "group",
                                  "enrollment", "tool_hash", "source_hash"])
def test_administrator_namespace_requires_current_enrolled_operator(monkeypatch, fault):
    enrollment = b"%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *\n"
    tool_raw = b"reviewed administrator tool"
    args = SimpleNamespace(boot_id="11111111-1111-4111-8111-111111111111", operator_user="runner",
                           operator_uid=1001, operator_gid=1002, operator_home="/home/runner",
                           source_sha256="a" * 64, tool_sha256=tool.digest(tool_raw))
    account = SimpleNamespace(pw_uid=1001, pw_gid=1002, pw_dir="/home/runner", pw_name="runner")
    if fault == "account":account.pw_uid = 1003
    if fault == "tool_hash":args.tool_sha256 = "0" * 64
    if fault == "source_hash":args.source_sha256 = "not-a-hash"
    monkeypatch.setattr(tool.os, "getuid", lambda: 1001 if fault == "root" else 0)
    monkeypatch.setattr(tool.os, "geteuid", lambda: 1001 if fault == "root" else 0)
    monkeypatch.setattr(tool.socket, "gethostname", lambda: "workstation" if fault == "host" else tool.HOST)
    monkeypatch.setattr(Path, "read_text", lambda self: "changed" if fault == "boot" else args.boot_id)
    monkeypatch.setattr(tool.pwd, "getpwnam", lambda user: account)
    monkeypatch.setattr(tool.pwd, "getpwuid", lambda uid: SimpleNamespace(
        pw_name="foreign" if fault == "reverse" else "runner"))
    monkeypatch.setattr(tool, "directory", lambda *a, **k: SimpleNamespace(
        st_uid=1003 if fault == "home" else 1001, st_gid=1002, st_mode=0o40755))
    monkeypatch.setattr(tool.grp, "getgrnam", lambda name: SimpleNamespace(gr_gid=1010))
    monkeypatch.setattr(tool.os, "getgrouplist", lambda *a: [] if fault == "group" else [1002, 1010])
    monkeypatch.setattr(tool, "read", lambda path, **k: (
        b"generic root grant" if fault == "enrollment" else enrollment) if path == tool.ENROLLMENT else tool_raw)
    if fault is None:
        operator, observed = tool.validate_namespace(args)
        assert operator == dict(user="runner", uid=1001, gid=1002, home="/home/runner") and observed == enrollment
    else:
        with pytest.raises(ValueError):tool.validate_namespace(args)


def test_duplicate_manifest_fields_are_refused():
    with pytest.raises(ValueError, match="duplicate"):
        json.loads('{"boot_id":"original","boot_id":"rebound"}', object_pairs_hook=tool.unique)
