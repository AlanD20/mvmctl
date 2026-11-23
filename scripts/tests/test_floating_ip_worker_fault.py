"""Pure admission and real lock-failure checks; never invokes the root observer."""

from copy import deepcopy
import fcntl
import json
import os
import signal
import stat
import time

import pytest

from tests.system.network.floating_ip_worker_fault import (
    _all_stopped, _bind_records, _continue_admitted_worker, _unique, _wait_lock,
)


@pytest.fixture
def bindings():
    owner, group, vm_id, boot = 1000, 1000, "a" * 32, "11111111-1111-4111-8111-111111111111"
    process = dict(pid=1234, start_time_ticks=100, expected_uid=0, expected_gid=0,
                   executable=dict(Device=1, Inode=2, SizeBytes=100, Mode=stat.S_IFREG | 0o755,
                                   UID=0, GID=0, Links=1, SHA256="a" * 64),
                   mount_device=1, mount_inode=3, network_device=1, network_inode=4, root_device=1, root_inode=5)
    worker = dict(schema=1, resource_owner_uid=owner, resource_owner_gid=group, vm_id=vm_id, boot_id=boot,
                  launch_generation=2, execution_uid=65000, generation=1, revision=3, phase="ready", process=process)
    instance = dict(schema_version=6, owner_uid=owner, vm_id=vm_id, network_placement="managed", lifecycle="active",
                    launch_generation=2, cleanup_generation=1,
                    launch=dict(host_boot_id=boot, execution_identity=dict(uid=65000)), active={})
    return worker, instance, owner, group, vm_id, boot


def test_exact_current_worker_binding_is_accepted(bindings):
    assert _bind_records(*bindings) is bindings[0]["process"]


@pytest.mark.parametrize("fault", ["schema", "owner", "group", "vm", "boot", "phase", "generation", "execution",
                                  "process_uid", "namespace", "executable_mode", "executable_link", "boolean_generation", "unknown_field"])
def test_root_worker_binding_refuses_rebound_or_unready_authority(bindings, fault):
    worker, instance, *identity = deepcopy(bindings)
    if fault == "schema":worker["schema"] = 2
    elif fault == "owner":worker["resource_owner_uid"] = 1001
    elif fault == "group":worker["resource_owner_gid"] = 1001
    elif fault == "vm":worker["vm_id"] = "b" * 32
    elif fault == "boot":instance["launch"]["host_boot_id"] = "other-boot"
    elif fault == "phase":worker["phase"] = "spawned"
    elif fault == "generation":instance["launch_generation"] = 3
    elif fault == "execution":instance["launch"]["execution_identity"]["uid"] = 65001
    elif fault == "process_uid":worker["process"]["expected_uid"] = 1000
    elif fault == "namespace":worker["process"]["network_inode"] = 0
    elif fault == "executable_mode":worker["process"]["executable"]["Mode"] |= 0o020
    elif fault == "executable_link":worker["process"]["executable"]["Links"] = 2
    elif fault == "boolean_generation":worker["generation"] = True
    else:worker["pid"] = 1234
    with pytest.raises(AssertionError):_bind_records(worker, instance, *identity)


def test_duplicate_root_authority_field_is_refused():
    with pytest.raises(AssertionError):json.loads('{"pid":1,"pid":2}', object_pairs_hook=_unique)


@pytest.mark.parametrize("states,expected", [([], False), (["T"], True), (["T", "t"], True),
                                            (["T", "S"], False), (["T", "R"], False), (["Z"], False)])
def test_every_worker_thread_must_be_stopped(states, expected):
    assert _all_stopped(states) is expected


def test_existing_authority_lock_timeout_never_unlocks_contender(tmp_path):
    path = tmp_path / "existing.lock"
    first = os.open(path, os.O_RDWR | os.O_CREAT | os.O_EXCL, 0o600)
    second = os.open(path, os.O_RDWR)
    try:
        fcntl.flock(first, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with pytest.raises(AssertionError, match="acquisition timed out"):
            _wait_lock(second, time.monotonic() + 0.01)
        with pytest.raises(BlockingIOError):fcntl.flock(second, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(first, fcntl.LOCK_UN)
        _wait_lock(second, time.monotonic() + 0.1)
    finally:
        os.close(second); os.close(first)


@pytest.mark.parametrize("fault", [None, "before", "signal", "after", "before-and-signal"])
def test_retained_pidfd_continuation_survives_proof_failure_and_preserves_signal_errors(monkeypatch, fault):
    process = {"pid": 987654}
    retained_pidfd = 73
    calls = []
    before_error, signal_error, after_error = OSError("executable unlinked"), OSError("pidfd signal failed"), OSError("proc unavailable")
    def no_numeric_pid_fallback(*args):
        pytest.fail("cleanup must never fall back to a numeric PID")
    monkeypatch.setattr(os, "kill", no_numeric_pid_fallback)
    def observe(actual_process, actual_pidfd):
        assert actual_process is process and actual_pidfd == retained_pidfd
        stage = "after" if any(call[0] == "signal" for call in calls) else "before"
        calls.append((stage, actual_pidfd))
        if stage == "before" and fault in ("before", "before-and-signal"):raise before_error
        if stage == "after" and fault == "after":raise after_error
        return ["T"] if stage == "before" else ["S"]
    def send(actual_pidfd, operation):
        calls.append(("signal", actual_pidfd, operation))
        assert (actual_pidfd, operation) == (retained_pidfd, signal.SIGCONT)
        if fault in ("signal", "before-and-signal"):raise signal_error
    if fault is None:
        _continue_admitted_worker(process, retained_pidfd, observe=observe, send=send)
    else:
        with pytest.raises(BaseExceptionGroup) as raised:
            _continue_admitted_worker(process, retained_pidfd, observe=observe, send=send)
        expected = {"before": [before_error], "signal": [signal_error], "after": [after_error],
                    "before-and-signal": [before_error, signal_error]}[fault]
        assert list(raised.value.exceptions) == expected
    assert [call for call in calls if call[0] == "signal"] == [("signal", retained_pidfd, signal.SIGCONT)]
    assert calls[0] == ("before", retained_pidfd)
    if fault not in ("signal", "before-and-signal"):
        assert calls[-1] == ("after", retained_pidfd)
