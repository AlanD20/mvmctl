# Active Improvements

This document tracks incomplete improvements and planned features.
For completed items, see [docs/IMPROVEMENTS_ARCHIVE.md](IMPROVEMENTS_ARCHIVE.md).

## Project Guidelines

When making these changes, ensure there will be **NO DEPRECATION messages/codes left over**. This project is under active development and IS NOT READY FOR PRODUCTION YET. Any changes that cause regression (such as renaming a command) are fine to proceed so long as all references/tests/docs are updated. You do not have to add code that allows migration from old to new approach.

---

## Implementation Reviews

- [ ] The `mvm config set|get` must modify values coming from constants.py file! any constants defined in the constants.py file, their value can be override by using `mvm config set|get <config_key>` where <config_key> is the variable defined in constants.py but in lowercase. These overrides are done in $MVM_CONFIG_DIR/config.json

## Core

- create a repl-like filesystem access to direct rootfs file via guestfs? this would be nice


## Networking

- [ ] Explore fully isolated bridge networking mechanism for vms.
- [ ] is it safer if we set the ip of the network (bridge) for 'source' for each tap device? since a tap device is always connected to a bridge and its IP is always part of the bridge's allocated subnet network?
Chain MVM-FORWARD (1 references)
 pkts bytes target     prot opt in     out     source               destination
    0     0 ACCEPT     all  --  mvm-default wlo1    172.35.0.0/24        0.0.0.0/0
    0     0 ACCEPT     all  --  wlo1   mvm-default  0.0.0.0/0            172.35.0.0/24
    0     0 ACCEPT     all  --  mvm-default mvm-def-p2-mpw  0.0.0.0/0            0.0.0.0/0
    0     0 ACCEPT     all  --  mvm-def-p2-mpw mvm-default  0.0.0.0/0            0.0.0.0/0


## Codebase Maintainability

- [ ] Ensure ALL 'yaml id' references in the code are replaced with internal id.
- [ ] overhaul of tools: ip/iptables etc... all must be centralized in utility and then it must be called from there. no implementation of binary usage inside core/api/cli! every tool must have its own helper utility file to centralize the source of truth

## CLI

- add confirmation to mvm cache prune .... including all sub commands.
- [ ] enable verbosity for every utility commands being used if we use --debug on the cli. for example mvm --debug ssh my-vm should add -v to the ssh command under the hood so that everything propagated properly with debugging scenarios!!! 
- [ ] rename configure.py to init.py

## Security

- change the db to user only and read for group only
- change cache folder permission!

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to IMPROVEMENTS_ARCHIVE.md when verified complete
- Keep this file focused on active work only

