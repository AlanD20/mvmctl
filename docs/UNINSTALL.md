# Uninstall mvmctl

The administrator uninstaller removes an initialized v0.3.0 installation and every enrolled operator's managed
data. This includes running VMs, snapshots, volumes, images, kernels, cached SSH keys and configuration. Export
anything you want to keep before continuing. Other operators must stop using mvmctl during the uninstall.

Use the scripts from the same source version as the installed binary. Python 3.10 or later is required;
`uninstall.py` and `uninstall_files.py` must stay together in `scripts/`.

Preview the selected paths first:

```bash
sudo python3 scripts/uninstall.py --dry-run
```

Then run the uninstall and type `UNINSTALL MVMCTL` when prompted:

```bash
sudo python3 scripts/uninstall.py
```

For unattended use, `--yes` supplies the same confirmation. It permanently deletes data; it is not a repair flag.
The dry run reads installation records without invoking mvm or creating a database.

## What it removes

- Every enrolled operator's default cache, plus existing active caches identified by protected-storage records.
- Default `~/.config/mvmctl` directories, including managed SSH-key copies, and `/tmp/mvmctl-UID` warm-image pools.
- Recognized personal `mvm` binaries or launchers in `~/.local/bin` and `~/bin`.
- Recorded host-init changes: mvm's sysctl and sudoers changes, enrolled memberships, and the group if mvm created it.
- The managed runtime directory, empty mvm cgroup, root authority under `/var/lib/mvmctl`, and installed binary.
  A recognized packaged `/usr/bin/mvm` is removed through its package manager.

External asset mirrors, original SSH files outside managed directories, and shared dependencies such as
Firecracker prerequisites remain installed. Preexisting group memberships and host settings are restored from
the administrator journal rather than guessed.

Custom paths that are not recorded cannot be discovered automatically. Add an operator's custom cache or
configuration directory explicitly; options may be repeated:

```bash
sudo python3 scripts/uninstall.py --dry-run \
  --cache alice=/srv/alice/mvm-cache --config alice=/srv/alice/mvm-config
```

Use the same options for the actual uninstall. Custom temporary directories outside these selected paths are
preserved. Paths must belong to the enrolled operator; symlinks, nested mounts, overlapping selections and
paths that would include the external default asset mirror are refused.

## If cleanup stops

The script uses `mvm cache clean --force` for each selected cache, then `mvm host reset` before deleting root
authority. A cleanup or ownership error stops the operation. Completed deletions are not rolled back; remaining
files and the error identify what needs attention. Do not replace the refusal with `rm -rf` or flush host firewall
rules. Unknown files, incompatible legacy layouts, changed administrator state and unresolved runtime ownership
need diagnosis with a compatible installation.

Once host reset completes, a retry can finish removing installation files. If the final directory removal is
interrupted, the retained root-only `/var/lib/.mvmctl-uninstall` may require administrator inspection; a retry
requires its reset records to remain intact.

After success, follow the [installation instructions](../README.md#installation) and enroll the operator again.
Managed VMs and artifacts start from an empty cache; preserved external mirrors can be reused.
