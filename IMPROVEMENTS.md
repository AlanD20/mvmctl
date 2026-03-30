# Active Improvements

This document tracks incomplete improvements and planned features.
For completed items, see [docs/IMPROVEMENTS_ARCHIVE.md](IMPROVEMENTS_ARCHIVE.md).

## Project Guidelines

When making these changes, ensure there will be **NO DEPRECATION messages/codes left over**. This project is under active development and IS NOT READY FOR PRODUCTION YET. Any changes that cause regression (such as renaming a command) are fine to proceed so long as all references/tests/docs are updated. You do not have to add code that allows migration from old to new approach.

---

## Implementation Reviews

- [ ] The `mvm config set|get` must modify values coming from constants.py file! any constants defined in the constants.py file, their value can be override by using `mvm config set|get <config_key>` where <config_key> is the variable defined in constants.py but in lowercase. These overrides are done in $MVM_CONFIG_DIR/config.json

## Networking

- [ ] Explore fully isolated bridge networking mechanism for vms.

## Codebase Maintainability

- [ ] Ensure ALL 'yaml id' references in the code are replaced with internal id.

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to IMPROVEMENTS_ARCHIVE.md when verified complete
- Keep this file focused on active work only
