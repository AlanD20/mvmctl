"""Non-root socket proof must include actual identity and a permission error."""

import errno

import pytest

from tests.system.network.floating_ip_helpers import _require_nonroot_socket_denial


@pytest.mark.parametrize("fault", [None, "eperm", "root", "groups", "capability", "missing_socket", "connected"])
def test_nonroot_socket_denial_requires_unprivileged_identity_and_dac_error(fault):
    item = dict(user="nobody", uids=[65534] * 4, gids=[65534] * 4, groups=[],
                capabilities=dict.fromkeys(("CapEff", "CapPrm", "CapInh", "CapAmb"), 0), errno=errno.EACCES)
    if fault == "eperm":item["errno"] = errno.EPERM
    elif fault == "root":item["uids"][2] = 0
    elif fault == "groups":item["groups"] = [0]
    elif fault == "capability":item["capabilities"]["CapEff"] = 1
    elif fault == "missing_socket":item["errno"] = errno.ENOENT
    elif fault == "connected":item["errno"] = 0
    if fault in (None, "eperm"):
        _require_nonroot_socket_denial(item)
    else:
        with pytest.raises(AssertionError):
            _require_nonroot_socket_denial(item)
