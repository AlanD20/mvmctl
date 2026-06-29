# Troubleshooting

Common issues and solutions when using `mvm`.

## Table of Contents

- [Permission denied: /dev/kvm](#permission-denied-devkvm)
- [Mixed Firewall Backend](#mixed-firewall-backend)
- [Firewall rules lost after reboot](#firewall-rules-lost-after-reboot)
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

## Mixed Firewall Backend

**Symptom:** VM has valid IP and gateway. ICMP (ping) works. TCP (curl/wget) times out.

**Detection:**

First check which firewall backend mvmctl is configured to use:
```bash
mvm config get settings firewall_backend
```

If the backend is `nftables` (default), verify the nftables ruleset contains mvm chains:
```bash
sudo nft list ruleset | grep -c "MVM-"
```

If the backend is `iptables`, check which iptables variant is active:
```bash
iptables --version
```

**Cause:** Docker and mvmctl use different firewall backends. Rules go to different places. mvmctl defaults to nftables; Docker may configure the system to use iptables-legacy, creating a split where rules are applied to the wrong backend.

**Fix:**
```bash
# Option 1: Sync firewall rules from the database
mvm network sync

# Option 2: Reboot host (clears all firewall state cleanly)
sudo reboot

# Option 3: Configure Docker to use the same backend as mvmctl
# Edit /etc/docker/daemon.json and restart Docker
```

Then re-run: `mvm host init`

---

## Firewall rules lost after reboot

**Problem:** After a reboot, VMs can't reach the network, or firewall chains are missing.

**Cause:** `mvm` does not persist firewall rules to system files (`/etc/nftables.conf`, `/etc/iptables/rules.v4`). Firewall rules are managed dynamically and must be reloaded after a reboot.

**Solution:**

Run `mvm network sync` after every reboot to reload all mvm firewall rules from the database:
```bash
mvm network sync
```

This restores all NAT, forwarding, and nocloud-net rules to the active firewall backend (nftables or iptables).

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

**1. Missing or broken jailer.** Firecracker requires the `jailer` binary alongside it. If the binary was fetched with `mvm bin pull`, it should be bundled, but manual installs may miss it.

**2. Kernel file is not readable by Firecracker.** Verify the kernel exists and the path is correct:
```bash
mvm kernel ls --json | jq -r '.[].path'
ls -l <path_from_output>
```

**3. Invalid boot arguments.** Custom boot args (set via `mvm config`) may contain typos or flags the kernel doesn't understand. Reset to defaults:
```bash
mvm config reset defaults.vm boot_args
```

**4. Socket path too long.** Firecracker uses Unix domain sockets which have a 108-character path limit. Long VM names or deep cache directories can exceed this. Check:
```bash
# See the VM's socket path
ls -la ~/.cache/mvmctl/vms/*/firecracker.api.socket
# If it looks very long, try a shorter VM name
```

**5. Binary / kernel architecture mismatch.** A kernel built for `x86_64` won't boot under an `aarch64` Firecracker binary. Verify both match:
```bash
file $(mvm kernel ls --json | jq -r '.[0].path')
mvm bin ls
```

---

## Undoing host init — `host clean` vs `host reset`

**Problem:** You want to roll back the changes made by `mvm host init`.

There are two levels of undo depending on how much you want to remove:

### `mvm host clean` — Remove networking only

Stop all running VMs first, then:

```bash
mvm host clean
```

This removes:
- All bridges and TAP devices
- All firewall chains used by mvm
- The default network from the database
- Any orphaned bridges and rules

It does **NOT** touch:
- The `mvm` system group or your user membership
- The sudoers drop-in file
- Sysctl `ip_forward` setting

Run `mvm host init` afterwards to recreate the default network.

### `mvm host reset` — Full factory reset

```bash
mvm host reset --force
```

Does everything `clean` does, **plus**:
- Removes the sudoers drop-in (`/etc/sudoers.d/mvm`)
- Removes your user from the `mvm` group
- Deletes the `mvm` system group
- Restores `net.ipv4.ip_forward` to its original value

After reset, run `mvm host init` from scratch to set everything up again.

> Both commands refuse to run if any VMs are still running. Stop them first with `mvm vm rm <name>`.

---

## NoCloud-net server failed to start

**Problem:** The HTTP server for cloud-init can't start.

**Solution:**

The port range (8000-9000) may be exhausted. Check for stale servers:
```bash
# List processes using nocloud ports
sudo ss -tlnp | grep -E ':(8[0-9]{3}|9[0-9]{3})'
# Kill any orphaned mvm processes
pkill -f "mvm run nocloudnet serve"
```

---

## VM can't fetch cloud-init data via nocloud-net

**Problem:** Cloud-init inside the VM can't reach the HTTP server.

**Solution:**

Verify firewall rules are configured:

For the nftables backend (default):
```bash
sudo nft list chain inet filter MVM-NOCLOUDNET-INPUT
# Should show rules allowing source IP to destination ports
```

For the iptables backend:
```bash
sudo iptables -L MVM-NOCLOUDNET-INPUT -n -v
# Should show rules allowing source IP to destination ports
```

Check that the VM's network is correctly set up. First find the nocloud server port and correct gateway from the VM logs:
```bash
mvm logs myvm --os | grep -i nocloud
```

Then test from within the VM (adjust subnet and port to what your config uses):
```bash
# Test connectivity to the default gateway (default subnet: 172.27.0.0/24)
ping -c 1 172.27.0.1
# Test HTTP access to nocloud server (port is dynamically allocated, check logs)
curl -v http://172.27.0.1:<port>/
```

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
# Detach the volume from the VM first
mvm volume detach <vm-identifier> <volume-name>

# Then remove the volume
mvm volume rm <volume-name>
```

Or force-remove (removes the volume record and file but does not hot-unplug from the VM):

```bash
mvm volume rm <volume-name> --force
```

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
