# Improvements Archive

This file contains completed improvements from the active development phase.
Items here have been implemented and verified. For active/incomplete items, see IMPROVEMENTS.md.

## Metadata (Completed)

- [x] the binaries>firecracker should not contain jailer_path
- [x] the binaries>firecracker should not contain active_binary_path
- [x] the binaries>jailer should not contain firecracker_path
- [x] the binaries>jailer should not contain active_binary_path
- [x] the binaries should contain defaults key where it has all the default binary paths like the following examples:
    - binaries>defaults>firecracker>binary_path, binaries>defaults>firecracker>full_version
    - binaries>defaults>jailer>binary_path, binaries>defaults>jailer>binary_path, binaries>defaults>jailer>full_version

## Cloud-Init (Completed)

- [x] cleanup_orphans function must be implemented in nocloud_net_manager.py file

## VM (Completed)

- [x] when user fetches/imports an image via `mvm image fetch/import`, the process at the end it must run 'blkid -p -s UUID -o value' on the final image that has only rootfs content, and then store the `fs_uuid` in the image's metadata. And then later when user enters `mvm vm create ...` the command must pull the `fs_uuid` of the image from metadata and use it as boot arg with root=UUID={fs_uuid}
- [x] **DISK SIZE FLAG SPECIFICATION**: Use `--disk-size` with short flag `-s`. Support single letter units: `512M` for MB, `1G` for GB (e.g., `--disk-size 512M`, `-s 1G`)
- [x] each image has `fs_type` in the metadata file, this type must be used in the boot arg of firecracker json file which `rootfstype={fs_type}`
- [x] introduce --kernel-path and --image-path to `mvm vm create` to allow custom image and kernel path
- [x] when `mvm vm create` throws an exception, it leaves out the directory creation of the vm state!
- [x] ensure root partition detection is available on both `mvm image fetch` and `mvm image import`
- [x] nocloud-net port from the flag is not passed dynamically in the code.
- [x] The vm state folder per vm must be changed from vm name to full sha for the folder name to make it unique even if duplicate names are being used. The full sha is the **64-character SHA256 hash** (not the 6-character short ID).
- [x] Add `mvm vm inspect <id|--name|-n` to view the metadata of the target vm

## Kernel (Completed)

- [x] user enters `mvm kernel fetch --official` then it will pull the kernel 6.19.9 official defined in kernels.yaml file! and then the config_fragmets is an array of either local path relative to assets folder or HTTP url where during the build it will apply these config files to kernel, and then later goes through enabled/disabled and set value keys to do those against the config!
- [x] `mvm kernel fetch` the effective_arch defaults to the `host machine arch` (e.g., x86_64), this is wrong, it must be default to x86_64 because the CLI has defined it! this does not have relevency with current host's architecture
- [x] when running `mvm kernel fetch --official` triggers the cache marker, the metadata is not updated for the kernel!

## CLI (Completed)

- [x] implement progress bar when fetching kernel/image/binary
- [x] firecracker failure and exit codes arent caught by `mvm vm ls`, it should appropriately follow and track if pid exist or exit out!
- [x] when running `ls` on EVERY SUPPORTED commands such as `mvm image ls`, etc.. it must read through the metadata and check if it has a path, file exist? if no, add (X) mark to indicate it's deleted and only metadata left.
- [x] when user enters `rm` for any resources such as kernel, image, vm, keys, etc.. it shouldn't prompt y/n. It MUST PROCEED WITH REMOVAL.
- [x] DO NOT ALLOW REMOVAL OF networks, images, kernels, if they are used by an active VM.
- [x] Add size to `kernel`, `image` ls commands
- [x] Add `*` to default resource names.
- [x] introduce `mvm cache init` and `mvm cache prune`
- [x] the `mvm configure` command must be renamed to `mvm init`

## Debugging (Completed)

- [x] introduce a new build type that enables debugging at finer grain for easier debugging issues
- [x] DEBUG MODE also introduces verbosity of errors, warnings, stack traces, etc..

## UI Improvements (Completed)

- [x] Running `mvm key add ~/.ssh/id_rsa.pub` - better error handling
- [x] Running `mvm network create` when user doesn't have proper permission

## Networking (Completed)

- [x] prompt user to provide the interface that provides internet for routing
- [x] When a new network is created, this is effectively a new bridge with its own subnet and every rule.
- [x] when `--ip` is passed to `mvm vm create` the IP isnt being checked against if this ip is already leased or free.

## Escalation (Completed)

- [x] handle privilege escalations exactly how `mvm network create` is handling it!

## Codebase Maintainability (Completed)

- [x] move values from constants.py to defaults.yaml for cloud-init
- [x] add the entire cloud-init config to --output-config with cloud-init as the key
- [x] move `mvm vm logs` to `mvm logs`
- [x] move `mvm vm ssh` to `mvm ssh`
- [x] change `cli/asset.py` to `cli/bin.py`

## Guestfs (Completed)

- [x] apply the aggressive optimization at docs/optimizations/guestfs-boot.md

## Console/Serial Access (Completed)

- [x] Console/serial access to VM without SSH: implemented in services/console_relay/, cli/console.py
