# Kubernetes with a delegated floating API address

Kubernetes v1.36, containerd and Cilium v1.19.3 (VXLAN, kube-proxy replacement) on six Firecracker VMs.
Three control-plane nodes provide the etcd majority needed to tolerate one controller failure. Two controllers cannot
retain a majority after either fails.

**Development example, not HA-qualified.** This workflow requires the integrated Guest Control worker, floating-IP
address reconciliation, finite kernel leases, env floating-IP/grant declarations and VM-to-VM execution admission.
Those foundations are being integrated; this example has not completed a live bootstrap or failover journey.
Bash/YAML checks and offline helper behavior checks do not establish runtime availability or a failover bound.
See [Guest Control authority](../../../docs/adr/0027-guest-control-authority.md) and
[floating-IP ownership](../../../docs/adr/0028-floating-ip-ownership.md).

| Nodes | Role | Primary IPv4 |
|---|---|---|
| controller-1, controller-2, controller-3 | control-plane + stacked etcd | 10.10.0.10, .11, .12 |
| worker-1, worker-2, worker-3 | worker | 10.10.0.20, .21, .22 |
| k8s-api | floating-IP resource, network `k8s` | 10.10.0.100 |

Each controller has exactly four grants for the immutable `k8s-api` resource: inspect, associate, renew and release.
Association and renewal each specify a **10s** lease in `k8s-controller.yaml`. To change the example lease, change all
six association/renewal values together before initial apply. This is an explicit example setting, not a product default.
The one-second loop uses authenticated `kubectl --server=https://PRIMARY:6443 get --raw=/readyz` with the controller's
admin kubeconfig. It never uses the VIP to decide whether the local API is healthy. Missing kubeconfig during bootstrap
means unhealthy; the service starts before `kubeadm init` and claims only when the local API becomes ready.

A healthy controller associates an unowned IP using the inspected generation, renews its own ownership, and waits
when another controller owns it. A failed probe stops renewal and attempts release. Every iteration inspects again;
conflicts, timeouts and unknown outcomes are never retried using stale mutation arguments. Recovery does not preempt
a healthy owner. The host kernel's timed key fences expired ownership even if a guest or worker stops running.
The loop selects ownership; it is not the fencing authority. There is no keepalived or VRRP exception.

## Run

Use a disposable runner sized for six VMs (12 GiB guest memory plus host headroom). Host prerequisites are an enrolled
candidate with the features above, `jq`, and `kubectl`. Build provisioning needs network access to Ubuntu, Kubernetes
and Cilium sources. Asset-consuming mvm workflows should use the existing mirror:

```bash
export MVM_ASSET_MIRROR="$HOME/.cache/mvm-asset-mirror"
cd examples/workflows/simple-k8s-cluster-ha
mvm env apply build-base.yaml
./apply.sh
./kubectl.sh get nodes
./destroy.sh
```

`build-base.yaml` builds the reusable `k8s-base` image. `apply.sh` applies controller resources/grants, worker resources,
and explicit traffic/exec policies, resolves the floating resource ID using root-backed `mvm floating-ip inspect`, and
passes the validated immutable ID through ordinary `mvm exec` to the fixed guest setup helper. Env command strings
cannot interpolate resource outputs. All three lease services start before initialization. Joining controllers and
workers use `mvm exec --allow-vm-exec` and `mvm-agent remote --user` with exact directional policies to controller-1.
Controller and worker join files have separate non-root reader groups, expire after two hours, and are never printed.
Failed certificate upload aborts without publishing a fabricated certificate key. Root-only init/join logs contain secrets.

`k8s-policy.yaml` declares network-to-floating-IP TCP 6443, directional controller-pair TCP 2379–2380,
controller-to-node TCP 10250, and directional node-pair UDP 8472 and TCP 4240. These are service-specific grants;
there is no blanket network allow. Host access to the VIP is still subject to the host's existing routing and firewall.
`kubectl.sh` uses the VIP exclusively, so a successful command exercises that endpoint. `kubeconfig.sh` fetches credentials
from any reachable controller through operator exec and writes a private local `.kubeconfig`.

`destroy.sh` stops and disables each lease loop, attempts current-generation release, then destroys policy, worker and
controller environments in reverse order. It stops on unreachable controllers or uncertain release rather than blindly
removing a live resource. Recover the controller and retry, or explicitly fence it through supported VM lifecycle
operations before performing reviewed cleanup. The base image remains reusable. Do not run raw env destroy while
lease loops are active.

## Qualification still required

A complete journey must bootstrap all six nodes, prove all three etcd members, exercise API requests through the VIP,
and verify one-controller failure while retaining quorum. Stop the actual API process or stop a controller VM; merely
stopping kubelet does not necessarily stop its static API pod. Observe current-generation ownership and service traffic
on the new owner, prove the old owner cannot source or receive VIP traffic after lease expiry, then restart the old
controller and verify that it does not preempt the healthy owner. Also exercise simultaneous claims, unknown outcomes,
worker death beyond lease expiry, and teardown. No fixed recovery time is promised by this example.
