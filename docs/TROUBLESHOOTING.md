# Troubleshooting

Common issues and solutions when using `mvm`.

## Table of Contents

- [Permission denied: /dev/kvm](#permission-denied-devkvm)
- [Managed firewall or network refusal](#managed-firewall-or-network-refusal)
- [Managed network state after host reboot](#managed-network-state-after-host-reboot)
- [VM won't boot / SSH times out](#vm-wont-boot-ssh-times-out)
- [VM won't start / Firecracker exits immediately](#vm-wont-start-firecracker-exits-immediately)
- [Undoing host init — host clean vs host reset](#undoing-host-init-host-clean-vs-host-reset)
- [NoCloud-net server failed to start](#nocloud-net-server-failed-to-start)
- [VM can't fetch cloud-init data via nocloud-net](#vm-cant-fetch-cloud-init-data-via-nocloud-net)
- [Console relay not working](#console-relay-not-working)
- [IP address exhaustion (no available IPs)](#ip-address-exhaustion-no-available-ips)
- [Cache corruption or stale state](#cache-corruption-or-stale-state)
- [Out of disk space for images or VMs](#out-of-disk-space-for-images-or-vms)
- [libguestfs / mvm cache init hangs forever](#libguestfs-mvm-cache-init-hangs-forever)
- [Cannot remove volume attached to a VM](#cannot-remove-volume-attached-to-a-vm)
- [VM boots slowly (SSH takes longer than 6-7 seconds)](#vm-boots-slowly-ssh-takes-longer-than-6-7-seconds)
- [`mvm cp` errors](#mvm-cp-errors)

---

## Permission denied: /dev/kvm

**Problem:** You get permission errors when trying to access KVM.

**Diagnosis:**

First check if `/dev/kvm` exists:
```bash
ls -l /dev/kvm
```

**Case 1 — `/dev/kvm` does not exist.** KVM kernel modules are not loaded:

```bash
sudo modprobe kvm
sudo modprobe kvm_intel    # or kvm_amd on AMD systems
# Verify it appeared
ls -l /dev/kvm
```

If `modprobe` fails, install the appropriate package (e.g. `linux-modules-extra-*` on Ubuntu/Debian, or ensure your kernel has `CONFIG_KVM` enabled).

**Case 2 — `/dev/kvm` exists but is not readable/writable by your user:**

```bash
sudo usermod -aG kvm $USER
# Log out and back in, then verify:
groups | grep kvm
```

---

## Managed firewall or network refusal

**Symptom:** Managed networking fails admission, or a VM cannot reach its intended destination.

v0.3.0 uses direct Go nftables transactions. Managed networking requires kernel support for both `inet` and
`bridge` families, connection tracking, and NAT; installing the `nft` tool cannot add missing kernel support.
Same-network and cross-network VM traffic starts denied. VM-to-VM TCP/UDP connectivity requires an applicable
traffic grant; successful vsock execution does not establish IP connectivity.

Inspect the affected managed network, VM, and traffic grants:

```bash
mvm network inspect <network-name> --json
mvm vm inspect <vm-name> --json
mvm policy traffic ls --json
```

For a refused operation, retain its structured error code and details. Root authority refuses foreign or changed
managed firewall state rather than overwriting it. Resolve the reported ownership or kernel-capability problem
before retrying; a ruleset flush or host reboot is not a reconciliation procedure.

See [networking and policy](RUNTIME.md#networking-policy-and-guest-execution) and
[traffic grants](REFERENCES.md#mvm-policy) for the supported contract.

---

## Managed network state after host reboot

**Problem:** Retained network or VM records need reconciliation after a host reboot.

Inspect the affected network and VM, then request managed-network authority reconciliation:

```bash
mvm network inspect <network-name> --json
mvm vm inspect <vm-name> --json
mvm network sync <network-name> --json
```

`network sync` reconciles managed network authority and retries pending creation. Inspect its result and any
reported failure; it does not promise restoration of arbitrary firewall rules or a NoCloud listener. Retained VM
records do not mean the old Firecracker process survived. Cleanup and relaunch must reconcile root-owned state,
and ambiguous ownership remains a refusal. See [runtime state](RUNTIME.md#state-and-process-layout).

---

## VM won't boot / SSH times out

**Problem:** VM appears to hang during boot.

**Solution:**

Cloud-init runs on first boot and takes 30–60 seconds regardless of the delivery method. Follow the console log to watch progress:
```bash
mvm logs myvm --follow
```

Look for cloud-init status messages like `Cloud-init v. X.X.X running modules...` to confirm it's working.

If it never reaches a `login:` prompt, check the Firecracker process log:
```bash
mvm logs myvm --os
```

---

## VM won't start / Firecracker exits immediately

**Symptom:** The VM is created but Firecracker exits right away — `mvm logs myvm --os` shows an error or empty log, and the VM never reaches the boot stage.

**Diagnosis:**

Start by checking the Firecracker log:
```bash
# Show Firecracker's internal log (not the VM serial console)
mvm logs myvm --os
```

**Common causes:**

**1. Missing, mismatched, or untrusted Jailer pair.** VM launch requires the exact checksum-verified Firecracker/Jailer release pair in the root-owned trusted store. Re-pull the selected release; source-built or manually copied binaries cannot launch jailed VMs:

```bash
mvm bin ls --long
mvm bin pull <version> --force
```

**2. cgroup v2 or required controllers are unavailable.** VM launch requires the unified cgroup-v2 hierarchy with `cpu`, `memory`, and `pids`. Refresh host information and inspect the detected limits:

```bash
mvm host info --refresh --json
```

If launch reports a verification mismatch, `mvm vm inspect <name> --json` shows the requested and observed values. mvmctl fails closed rather than launching without the envelope.

**3. Kernel file is not readable by Firecracker.** Verify the kernel exists and the path is correct:
```bash
mvm kernel ls --json | jq -r '.data[].path'
ls -l <path_from_output>
```

**4. Invalid boot arguments.** Custom boot args (set via `mvm config`) may contain typos or flags the kernel doesn't understand. Reset to defaults:
```bash
mvm config reset defaults.vm boot_args
```

**5. Binary / kernel architecture mismatch.** A kernel built for `x86_64` won't boot under an `aarch64` Firecracker binary. Verify both match:
```bash
file $(mvm kernel ls --json | jq -r '.data[0].path')
mvm bin ls
```

---

## Undoing host init — `host clean` vs `host reset`

**Problem:** You want to roll back the changes made by `mvm host init`.

Caller-scoped network cleanup and administrator rollback have different scopes. Neither command is a supported
legacy-state migration or removal procedure. See the [host command reference](REFERENCES.md#mvm-host).

### `mvm host clean` — Remove networking only

Stop the caller's running VMs first, then run as the enrolled operator:

```bash
mvm host clean
```

`host clean` observes the caller's root VM inventory and removes its locally registered networks through typed
root authority. It refuses running VMs or incomplete observation and does not force-stop VMs or reset shared
host settings, operator membership, or the sudoers policy. Recreate desired networks with `mvm network create`;
network cleanup does not undo administrator enrollment.

### `mvm host reset` — Administrator rollback

Reset requires exclusive host-wide quiescence and compatible recorded administrator state. Run through the
installed root-owned binary with administrator access:

```bash
sudo /usr/local/bin/mvm host reset --force
```

Reset restores recorded host settings and enrollment changes through retryable receipts. It refuses live or
ambiguous state, preserves authority history, and leaves privileged admission disabled afterward, including
after a restoration error. `--force` does not bypass quiescence or force-stop VMs.

To set up the host and enroll the invoking non-root operator again:

```bash
sudo /usr/local/bin/mvm host init
```

Log out and back in, or run `newgrp mvm`, if the current session lacks the enrolled group. In a direct-root
session, both init and reset require `--operator USER`; sudo invocation selects its invoking operator and
rejects that flag.

---

## NoCloud-net server failed to start

**Problem:** The HTTP server for cloud-init can't start.

**Solution:**

NoCloud net mode binds the selected managed network's gateway on fixed TCP port `8000`. Custom ports are
rejected, and a listener collision fails without adopting or killing the existing listener. Inspect the network
and the listener at its actual gateway address:

```bash
mvm network inspect <network-name> --json
sudo ss -ltnp 'sport = :8000'
```

Identify the listener's owning workload before changing it. A successful bind must precede the typed fixed-slot
service grant; the grant reserves gateway TCP `8000` and does not authenticate the listener process.
See [NoCloud net mode](REFERENCES.md#how-net-mode-works).

---

## VM can't fetch cloud-init data via nocloud-net

**Problem:** Cloud-init inside the VM can't reach the HTTP server.

**Solution:**

Confirm the VM uses a managed network and net cloud-init mode; networkless placement cannot use net mode.
Inspect the network's actual gateway, its fixed TCP `8000` listener, and the VM's cloud-init messages:

```bash
mvm network inspect <network-name> --json
sudo ss -ltnp 'sport = :8000'
mvm logs myvm | grep -i nocloud
```

From the affected guest, diagnose HTTP delivery using the actual gateway address:

```bash
curl -v http://<gateway-ip>:8000/meta-data
```

Keep listener readiness separate from the root-derived service grant: a listening socket alone does not prove
the grant is applied, and a retained grant does not prove the listener is running. Review any typed bind or grant
failure before retrying. See [NoCloud security](REFERENCES.md#net-mode-security-architecture).

---

## Console relay not working

**Problem:** Can't attach to VM console.

**Solution:**

Check if the console relay is running:
```bash
mvm console myvm --state
```

If not running, try restarting it:
```bash
mvm console myvm --kill
mvm console myvm
```

---

## IP address exhaustion (no available IPs)

**Symptom:** A VM creation fails with: `No available IPs in subnet`, or a new VM boots but can't get network connectivity.

**Cause:** The default network uses a `/24` subnet (`172.27.0.0/24`), which provides 253 usable IPs. Each VM consumes one IP on the bridge network via first-fit allocation. If you create enough VMs to exhaust the pool, new VMs cannot get an IP.

**Solution:**

Check how many IPs are in use:
```bash
mvm network inspect <network_name>
```

If the subnet is full, you have two options:

**Option 1 — Remove unused VMs** to free their IP leases:
```bash
mvm vm ls                    # List all VMs
mvm vm rm <old-vm>    # Remove unneeded VMs
```
IPs are released automatically when VMs are removed.

**Option 2 — Create a second network** with a larger or different subnet:
```bash
mvm network create secondary --subnet 10.0.0.0/16
```
Then create VMs on it with `--network secondary`.

---

## Cache corruption or stale state

**Problem:** Weird behavior, metadata out of sync with actual files.

**Solution:**

Prune stale cache entries:
```bash
mvm cache prune vm --dry-run  # Preview what would be removed
mvm cache prune vm            # Actually remove stale entries
```

For a complete reset (removes all VMs):
```bash
mvm vm ls                  # Check what VMs exist
mvm cache prune --all      # Prune everything
```

---

## Out of disk space for images or VMs

**Symptom:** Image downloads fail mid-way, VM creation fails with disk errors, or `df` shows the cache partition is full.

**Cause:** VM images are typically 1-3 GB each, and each VM clones its root filesystem for isolation. A few VMs can easily consume 10-20 GB.

**Diagnosis:**

Check the cache directory size:
```bash
du -sh ~/.cache/mvmctl/
du -sh ~/.cache/mvmctl/images/
du -sh ~/.cache/mvmctl/vms/
```

Check overall disk usage:
```bash
df -h ~/.cache/mvmctl/
```

**Solution:**

**1. Remove unused images** — Images you're not actively using can be re-fetched later:
```bash
mvm image ls                         # List cached images
mvm image rm <image-id>              # Remove unused one
```

**2. Remove unused kernels** — Unneeded kernel builds consume space:
```bash
mvm kernel ls
mvm kernel rm <kernel-id>
```

**3. Remove stopped VMs** — Each stopped VM still holds its cloned rootfs:
```bash
mvm vm ls                            # List VMs
mvm vm rm <vm-name>           # Remove VM and its disk
```

**4. Purge the cache (nuclear option):**
```bash
mvm cache prune --all                # Remove all cached artifacts
```

> The cache directory defaults to `~/.cache/mvmctl/`. To change it, set the `MVM_CACHE_DIR` environment variable to point to a partition with more space.

---

## libguestfs / `mvm cache init` hangs forever

**Symptom:** Commands that build the libguestfs appliance freeze indefinitely:
```bash
libguestfs-make-fixed-appliance ~/.cache/mvmctl/appliance   # hangs
mvm cache init                                               # hangs
guestfish -a /dev/null run                                   # hangs
```

The serial log (if captured) shows:
```
supermin: waiting another 1024000000 ns for root UUID to appear
```
repeating forever with **no block devices** under `/sys/block/`.

**Root cause:** The libguestfs appliance builder (`supermin` / `libguestfs-make-fixed-appliance`) automatically scans all installed kernels and selects the one with the **highest version string**. It does not ask — it simply picks the newest kernel it finds and uses it to boot the appliance QEMU microVM. If that kernel was built with `CONFIG_VIRTIO_PCI` disabled, the appliance cannot see its block device and hangs forever.

Here is the chain of failure:

1. `libguestfs-make-fixed-appliance` iterates `/lib/modules/*`, picks the kernel with the latest version.
2. It boots that kernel inside a QEMU microVM using `virtio-scsi-pci` to expose the appliance disk.
3. If the selected kernel lacks `CONFIG_VIRTIO_PCI`, the virtio PCI device never materializes.
4. No SCSI host appears -> no block devices under `/sys/block/` -> supermin's initrd spins forever waiting for the root disk UUID.

**Why a stock kernel is installed but still not used:**

This is the trap. You may have both a stock `linux` kernel and a custom kernel (e.g. `linux-g14`, `linux-zen`, `linux-custom`) installed. Because the custom kernel often shares the same base version (e.g. both are `6.12.x-arch1`), the appliance builder may sort it first — or simply pick whichever appears latest in the module directory. If the custom kernel disables VirtIO (common in bare-metal-optimized builds that strip virtualization drivers), the appliance **silently picks the broken kernel** and hangs. The stock kernel is on disk but never tried.

**This is a general caution:** any kernel variant that strips VirtIO or PCI pass-through support will break libguestfs — not just `linux-g14`. If you use a custom kernel, always verify it has `CONFIG_VIRTIO_PCI` before assuming libguestfs will work.

> **Note on `mvm cache init` kernel detection:** `mvm cache init` attempts to find a suitable kernel for the appliance by scanning `/boot/` for kernels, extracting their versions, and scoring them based on virtio module availability. It sets `SUPERMIN_KERNEL` and `SUPERMIN_MODULES` environment variables to guide `libguestfs-make-fixed-appliance`. However, this detection is heuristic — it relies on virtio modules being present on disk and may still select an unsuitable kernel in complex multi-kernel environments (e.g. chroots, container builds, or unusual boot layouts). If the appliance build hangs despite having a stock kernel installed, the detection may have picked the wrong one.

**Diagnosis:** Check if your currently running kernel supports VirtIO:

```bash
# Check running kernel config
zcat /proc/config.gz 2>/dev/null | grep CONFIG_VIRTIO_PCI || \
  grep CONFIG_VIRTIO_PCI /boot/config-$(uname -r) 2>/dev/null

# Expected (working):
# CONFIG_VIRTIO_PCI=y
# or
# CONFIG_VIRTIO_PCI=m

# Problematic:
# # CONFIG_VIRTIO_PCI is not set

# Also verify the module exists on disk
find /lib/modules/$(uname -r) -name "virtio_pci*" 2>/dev/null
# Should return: /lib/modules/.../virtio_pci.ko.zst
```

**But the running kernel may not be the one the appliance uses.** To see which kernel the appliance builder will actually select:

```bash
# Show all installed kernels, sorted by version (the appliance picks the last one)
ls -1v /lib/modules/
```

If the kernel listed last lacks VirtIO support, that is the one causing the hang — even if `uname -r` shows a working kernel.

**Fix:** Ensure a kernel **with** VirtIO support is the highest-versioned installed kernel (or remove the broken one).

```bash
# 1. Install a kernel with VirtIO support (keep your current kernel until verified)
sudo pacman -S linux linux-headers

# 2. Update bootloader
# For GRUB:
sudo grub-mkconfig -o /boot/grub/grub.cfg
# For systemd-boot:
sudo reinstall-kernels

# 3. Reboot and select the stock kernel entry
sudo reboot

# 4. Verify you're on the new kernel
uname -r

# 5. Verify the appliance will pick the right kernel
ls -1v /lib/modules/
# The last entry should come from the stock linux package, not your custom one

# 6. Re-run the diagnosis commands above to confirm virtio is available on that kernel

# 7. Test libguestfs
libguestfs-make-fixed-appliance ~/.cache/mvmctl/appliance

# 8. Only after confirming everything works, remove the custom kernel
# sudo pacman -R linux-g14 linux-g14-headers   # example for ASUS G14 custom kernel
```

**Alternative workaround (without removing your custom kernel):** If you want to keep your custom kernel as the boot default but need libguestfs to use the stock kernel, you can temporarily make the stock kernel the highest version by giving it a higher `INSTALLED` file value — but this is fragile and distribution-specific. The safest approach is to remove (or not install) custom kernels that strip VirtIO support.

---

## Cannot remove volume attached to a VM

**Problem:** `mvm volume rm` fails because the volume is attached to a running VM.

**Solution:**

```bash
# Stop the VM, then detach its volume
mvm vm stop <vm-identifier>
mvm volume detach <vm-identifier> <volume-name>

# Then remove the volume
mvm volume rm <volume-name>
```

To remove a volume that remains attached, stop its VM first, then use `--force`:

```bash
mvm vm stop <vm-identifier>
mvm volume rm <volume-name> --force
```

`--force` bypasses the attachment refusal only for a VM authoritatively observed as stopped; it never hot-unplugs
a running guest. To grow an attached volume, stop the VM but leave the volume attached, then run
`mvm volume resize <volume-name> <new-size>` before starting the VM again.

---

## VM boots slowly (SSH takes longer than 6-7 seconds)

If a VM takes significantly longer than expected to become reachable via SSH (more than 6-7 seconds from `mvm vm start`), the image may be using a DHCP client that our provisioning did not successfully disable.

MicroVM bridges have no DHCP server — the kernel `ip=` boot parameter provides a static IP. Some cloud images configure their network interface to use DHCP by default, and the DHCP client (dhcpcd, dhclient, systemd-networkd) waits for a lease that never arrives, falling through to IPv4LL after a timeout. This adds 10-20 seconds to boot.

The Alpine provisioner handles this by:
1. Setting `iface eth0 inet manual` in `/etc/network/interfaces`
2. Adding `denyinterfaces eth0` to `/etc/dhcpcd.conf`

If a different image (not Alpine) exhibits slow boot, check whether the image uses `dhcpcd`, `dhclient`, or `systemd-networkd` and disable DHCP on the primary network interface. Open a PR to add provisioning support for that image type.

```bash
# Check if the VM's network interface is using DHCP
mvm ssh <vm-name> --cmd "cat /etc/network/interfaces 2>/dev/null || cat /etc/netplan/*.yaml 2>/dev/null || echo 'check systemd-networkd'"

# Check if dhcpcd is running and slowing boot
mvm ssh <vm-name> --cmd "ps aux | grep dhcpcd | grep -v grep"
```

---

## `mvm cp` errors

### CPError on copy failure

**Symptom:** `mvm cp` reports an error when the destination already has a file.

**Solution:**
```bash
# Use --force to overwrite
mvm cp --force ./myfile.txt my-vm:/root/

# Or remove the existing file first
mvm ssh my-vm --cmd "rm /root/myfile.txt"
```

### CPError on missing destination directory

**Symptom:** Host → VM copy fails when the destination path is not a directory.

**Solution:**

For single-source copies, the destination is treated as a directory if it ends with `/` or if the remote path already exists as a directory. For multiple sources, the destination must end with `/`:
```bash
# Single source — directory mode (preserves source filename, trailing / optional)
mvm cp ./myfile.txt my-vm:/root/

# Single source — file mode (writes to exact path, no trailing /)
mvm cp ./myfile.txt my-vm:/root/custom-name.txt

# Multiple sources — destination must end with /
mvm cp ./a.txt ./b.txt my-vm:/dst/
```

> For multi-source copies the destination **must** end with `/` — this is enforced
> on the host side. For single-source copies, the guest agent stats the destination
> and switches to directory mode if the path exists as a directory or ends with `/`.

### CPError: VM not found or no vsock

**Symptom:** Copy fails with "Could not resolve VM" or "VM has no vsock configuration".

**Solution:**
- Verify the VM name is correct: `mvm vm ls`
- Check the VM is running and has vsock enabled: `mvm vm inspect <name>`
- The VM must be in RUNNING state with a valid vsock configuration for file copy to work

---

Still having issues? [Open an issue on GitHub](https://github.com/AlanD20/mvmctl/issues) with:
- The command you ran
- The full error output (with `MVM_LOG_LEVEL=DEBUG` if possible)
- Your OS and mvm version (`mvm --version`)
