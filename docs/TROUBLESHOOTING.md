# Troubleshooting

Common issues and solutions when using `mvm`.

---

## Permission denied: /dev/kvm

**Problem:** You get permission errors when trying to access KVM.

**Solution:**
```bash
sudo usermod -aG kvm $USER
# Log out and back in, then verify:
groups | grep kvm
```

---

## Mixed iptables Backend

**Symptom:** VM has valid IP and gateway. ICMP (ping) works. TCP (curl/wget) times out.

**Detection:**
```bash
# Check which backend iptables uses
iptables --version

# Check if iptables-legacy has active rules (pkts > 0)
sudo iptables-legacy -L -n -v
```

**Cause:** Docker and mvmctl use different iptables backends. Rules go to different places.

**Fix:**
```bash
# Option 1: Clear orphaned legacy rules (quick fix)
sudo iptables-legacy -F

# Option 2: Reboot host (clears both backends cleanly)
sudo reboot

# Option 3: Configure Docker to use same backend as mvmctl
# Edit /etc/docker/daemon.json and restart Docker
```

Then re-run: `mvm host init`

---

## Bridge mvm-default not found / No such device

**Problem:** Network bridge doesn't exist when creating a VM.

**Solution:**

Run `sudo mvm host init` once; the bridge is auto-created when you create a VM.

---

## Kernel not found

**Problem:** No kernel available for VM creation.

**Solution:**
```bash
mvm kernel fetch --type firecracker
```

---

## VM won't boot / SSH times out

**Problem:** VM appears to hang during boot.

**Solution:**

Cloud-init runs on first boot and takes 30–60 seconds. Follow the console log:
```bash
mvm logs myvm --follow
```

If it never reaches a `login:` prompt, check the Firecracker process log:
```bash
mvm logs myvm --os
```

---

## Image not found

**Problem:** The image ID you specified isn't available.

**Solution:**
```bash
mvm image fetch ubuntu-24.04
mvm image ls   # ✓ should appear
```

---

## Firecracker binary not found

**Problem:** No Firecracker binary available.

**Solution:**
```bash
mvm bin fetch 1.15.0
mvm bin default <id>
```

---

## host init has not been run

**Problem:** Privilege setup incomplete.

**Solution:**

Run `sudo mvm host init` first to set up the `mvm` group and sudoers configuration.

---

## NoCloud-net server failed to start

**Problem:** The HTTP server for cloud-init can't start.

**Solution:**

The port range (8000-9000) may be exhausted. Check for stale servers:
```bash
# List processes using nocloud ports
sudo ss -tlnp | grep -E ':(8[0-9]{3}|9[0-9]{3})'
# Kill any orphaned mvm processes
pkill -f nocloud-net-server
```

---

## VM can't fetch cloud-init data via nocloud-net

**Problem:** Cloud-init inside the VM can't reach the HTTP server.

**Solution:**

Verify firewall rules are configured:
```bash
sudo iptables -L MVM-NOCLOUDNET-INPUT -n -v
# Should show rules allowing source IP to destination ports
```

Check that the VM's network is correctly set up:
```bash
# From within the VM, test connectivity to the gateway
ping -c 1 10.0.0.1
# Test HTTP access to nocloud server
curl -v http://10.0.0.1:8080/
```

---

## Cloud-init seems slow

**Problem:** First boot takes longer than expected.

**Solution:**

This is normal. Cloud-init takes 30-60 seconds on first boot regardless of the delivery method. To monitor progress:
```bash
mvm logs myvm --follow
```

Look for cloud-init status messages like `Cloud-init v. X.X.X running modules...`

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

## Network creation fails with permission denied

**Problem:** Can't create networks without sudo.

**Solution:**

Make sure you've run `sudo mvm host init` and are in the `mvm` group:
```bash
# Check group membership
groups | grep mvm

# If not in group, add yourself
sudo usermod -aG mvm $USER
# Log out and back in
```

---

## Cache corruption or stale state

**Problem:** Weird behavior, metadata out of sync with actual files.

**Solution:**

Prune stale cache entries:
```bash
mvm cache prune vm --dry-run  # Preview what would be removed
mvm cache prune vm            # Actually remove stale entries
```

For a complete reset (⚠️ removes all VMs):
```bash
mvm vm ls                  # Check what VMs exist
mvm cache prune --all      # Prune everything
```

---

## Debug mode

For more detailed error output, set debug mode:

```bash
# Set in config
mvm config set debug enabled true

# Or use environment variable
MVM_LOG_LEVEL=DEBUG mvm vm create --name myvm --image ubuntu-24.04
```

---

Still having issues? [Open an issue on GitHub](https://github.com/AlanD20/mvmctl/issues) with:
- The command you ran
- The full error output (with `MVM_LOG_LEVEL=DEBUG` if possible)
- Your OS and mvm version (`mvm --version`)
