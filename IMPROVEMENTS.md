# MANDATORY INSTRUCTIONS TO BE FOLLOWED
When making these changes. Ensure that there will be NO DEPRECATION messages/codes are left over. This project is under active development and IS NOT READY FOR PRODUCTION YET. THEREFORE any changes that causes regression such as renaming a command to something else, it's fine to proceed so long as all references/tests/docs are updated. You do not have to add codes that allows migration from old to new approach.

# Metadata

- [x] the binaries>firecracker should not contain jailer_path
- [x] the binaries>firecracker should not contain active_binary_path
- [x] the binaries>jailer should not contain firecracker_path
- [x] the binaries>jailer should not contain active_binary_path
- [x] the binaries should contain defaults key where it has all the default binary paths like the following examples:
    - binaries>defaults>firecracker>binary_path, binaries>defaults>firecracker>full_version
    - binaries>defaults>jailer>binary_path, binaries>defaults>jailer>full_version

# cloud-init

- [x] cleanup_orphans function must be implemented in nocloud_net_manager.py file

# VM

- [x] when user fetches/imports an image via `mvm image fetch/import`, the process at the end it must run 'blkid -p -s UUID -o value' on the final image that has only rootfs content, and then store the `fs_uuid` in the image's metadata. And then later when user enters `mvm vm create ...` the command must pull the `fs_uuid` of the image from metadata and use it as boot arg with root=UUID={fs_uuid}
- [x] **DISK SIZE FLAG SPECIFICATION**: Use `--disk-size` with short flag `-s`. Support single letter units: `512M` for MB, `1G` for GB (e.g., `--disk-size 512M`, `-s 1G`)
- [x] each image has `fs_type` in the metadata file, this type must be used in the boot arg of firecracker json file which `rootfstype={fs_type}`
- ~~firecracker rootfs requires integrating the ssh key into the image! need to figure out a way to do this? perhaps create a copy of an image by integrating a file?~~ **REMOVED: Feature deferred to post-4-week timeline**
- [x] introduce --kernel-path and --image-path to `mvm vm create` to allow custom image and kernel path
- [x] when `mvm vm create` throws an exception, it leaves out the directory creation of the vm state!
- [x] ensure root partition detection is available on both `mvm image fetch` and `mvm image import`
- [x] nocloud-net port from the flag is not passed dynamically in the code.
- [x] The vm state folder per vm must be changed from vm name to full sha for the folder name to make it unique even if duplicate names are being used. The full sha is the **64-character SHA256 hash** (not the 6-character short ID).
    - **NOTE**: NO MIGRATION FROM LEGACY FOLDER NAME TO NEW FOLDER NAME IS REQUIRED. Project is under active development, changes will not break anything. When implemented, clear the cache and perform manual QA to ensure everything works.
- [x] Add `mvm vm inspect <id|--name|-n` to view the metadata of the target vm

# Kernel

- [x] user enters `mvm kernel fetch --official` then it will pull the kernel 6.19.9 official defined in kernels.yaml file! and then the config_fragmets is an array of either local path relative to assets folder or HTTP url where during the build it will apply these config files to kernel, and then later goes through enabled/disabled and set value keys to do those against the config!
- [x] `mvm kernel fetch` the effective_arch defaults to the `host machine arch` (e.g., x86_64), this is wrong, it must be default to x86_64 because the CLI has defined it! this does not have relevency with current host's architecture
- [x] when running `mvm kernel fetch --official` triggers the cache marker, the metadata is not updated for the kernel!

# CLI

- [x] implement progress bar when fetching kernel/image/binary
    - **PROGRESS BAR REQUIREMENTS**: Must be **ASCII text-based**, do NOT use Rich Progress API or any graphical progress bars. Use simple text output like: `[####      ] 45%` or `Downloading... 45% (4.2MB/10MB)`
    - In non-TTY environments (CI/scripts), progress bars should be disabled or show simple text progress
- [x] firecracker failure and exit codes arent caught by `mvm vm ls`, it should appropriately follow and track if pid exist or exit out! each vm has `firecracker.pid` in the vm folder state. use this pid to track! and ideally a way to show the exit code in the `status` field.
- [x] when running `ls` on EVERY SUPPORTED commands such as `mvm image ls`, etc.. it must read through the metadata and check if it has a path, file exist? if no, add (X) mark to indicate it's deleted and only metadata left. if it's a network bridge, check if it's still there. etc.. the state check with actual environment depends on the command, for image, kernel, vm, key, bin, they are file state checks, but for network, it's a check with the actual bridge if it still exists!
- [x] when user enters `rm` for any resources such as kernel, image, vm, keys, etc.. it shouldn't prompt y/n. It MUST PROCEED WITH REMOVAL.
- [x] DO NOT ALLOW REMOVAL OF networks, images, kernels, if they are used by an active VM. The CLI must utilize the metadata to ensure there isnt an active VM using these.
- [x] Add size to `kernel`, `image` ls commands
- [x] Add `*` to default resource names. This applies to `mvm kernel|image|key|network` commands! if there is a field for default when running `ls` remove it and use `* ` as a prefix for the name of the resource. **Format is asterisk + space prefix** (e.g., "* ubuntu-24.04").
- [x] introduce `mvm cache init` and `mvm cache prune` where the `cache_init` function executes functions. DO NOT IMPLEMENT caching within this function, each caching mechanism must have their own function to do the caching and clear the caching. This will make the cache_init more maintainable and exactly define what is being cached. Currently caching is only done for guestfs appliance.
    - **MODULAR FUNCTION DEFINITION**: Define separate functions for each resource: `cache_init_guestfs_appliance()`, `cache_init_vms()`, `cache_init_images()`, `cache_init_kernels()`, `cache_init_networks()` for initialization. Define `cache_prune_guestfs_appliance()`, `cache_prune_vms()`, `cache_prune_networks()`, `cache_prune_images()`, `cache_prune_kernels()` for pruning.
    - the `mvm cache prune` must also call the invalidation of each cache by invoking their functions. NO DIRECT INVALIDATION IS IMPLEMENTED WITHIN THIS FUNCTION. currently cache pruning will remove: guestfs appliance from cache folder, remove all VMs that are not in `running` state, remove images that do not have any reference from an active vm, remove kernels that do not have any reference from an active vm, remove networks that do not have any reference from an active vm. Each of these must have their own function such as cache_prune_networks() to only prune stale networks, etc...
    - **EXTENDED REQUIREMENTS**:
        - [x] Add per-resource prune commands: `mvm cache prune vm` to only prune VMs, `mvm cache prune network` to prune unused networks, `mvm cache prune image` to prune unused images, `mvm cache prune kernel` to prune unused kernels
        - [x] Add `--include-stopped` flag to include stopped VMs in pruning (by default, only ERROR state VMs are pruned)
        - [x] Add `--include-running` flag to include running VMs (use with caution)
        - [x] Add `--all|-a` flag to prune everything (VMs, networks, images, kernels, guestfs) but shows user prompt to confirm before proceeding
- [x] the `mvm configure` command must be renamed to `mvm init`. and it must call `mvm host init` and `mvm cache init`, remove the user prompts to download image/kernel/keys. REMINDER, PROJECT IS UNDER ACTIVE DEVELOPMENT.
    - **EXTENDED REQUIREMENTS**:
        - [x] `mvm init` must be **interactive by default** - runs `mvm host init` and `mvm cache init`, then asks user which firecracker binary version to download
        - [x] If `--non-interactive` flag is passed, it downloads the **latest** firecracker binary version automatically without prompting
        - This is designed for quick user onboarding

# Implementation reviews

- [x] the constants.py file is the source of entire application's global configuration, this global configuration is looking into $MVM_CONFIG_DIR/config.json file, if the variable does not exist there, it resolves the defaults.yaml file! This is very critical to have because later the `mvm config set|get` will need to modify $MVM_CONFIG_DIR/config.json file to override these global configuration values.
- [ ] The `mvm config set|get` must modify values coming from constants.py file! any constants defined in the constants.py file, their value can be override by using `mvm config set|get <config_key>` where <config_key> is the variable defined in constants.py but in lowercase. These overrides are done in $MVM_CONFIG_DIR/config.json

# Debugging

Complete overhaul of every single path of the CLI application and handle any user facing error gracefully as a friendly output. DO NOT SHOW EXCEPTIONS and STACK TRACES, unless DEBUG MODE is enabled. Every single path of the entire application must support this mode, ensure that every single path of the code has this particularly more emphasis on complex logics requiring sequential state in order to allow the cli application to perform an action.
- [x] introduce a new build type that enables debugging at finer grain for easier debugging issues. a value defined in constants.py file where it derives the DEBUG_MODE value from defaults.yaml file! this will enable debug mode throughout the cli application.
    - **DEBUG_MODE DEFAULT STRUCTURE** (add to defaults.yaml):
        ```yaml
        debug:
          enabled: false
          verbose_errors: false
          show_tracebacks: false
        ```
    - Debug mode is **OFF by default** (`enabled: false`)
- [x] DEBUG MODE also introduces verbosity of errors, warnings, stack traces, etc.. to improve debugging throughout the application when an error occurs.

# Following UI errors must be more friendlier for user

- [x] Running `mvm key add ~/.ssh/id_rsa.pub`
- [x] Running `mvm network create` when user doesn't have proper permission

# Networking

- [x] prompt user to provide the interface that provides internet for routing
- [x] When a new network is created, this is effectively a new bridge with its own subnet and every rule. in `chain POSTROUTING` the target for this bridge must only allow `source` for the cidr provided only! and the `out` value must be the bridge interface name! for example a new network called `mvm-test` with cidr 175.39.0.0/24. the expected `source` value is 175.39.0.0/24 and `out` value is !mvm-test when the target is `MVM-POSTROUTING` chain.
- [x] when `--ip` is passed to `mvm vm create` the IP isnt being checked against if this ip is already leased or free. the application must show a friendly error that given ip on given network name is already reserved.
- [ ] Explore fully isolated bridge networking mechanism for vms.

# Escalation

- [x] handle privilege escalations exactly how `mvm network create` is handling it!

# Codebase Maintainability

- [ ] Ensure ALL 'yaml id' references in the code are replaced with internal id.
- [x] move values from constants.py to defaults.yaml for cloud-init
- [x] add the entire cloud-init config to --output-config with cloud-init as the key, then under it all the user-data, meta-data, network-config
- ~~config_gen.py line 227 must come from defaults.yaml!!~~ **REMOVED: Skip this requirement**
- [x] move `mvm vm logs` to `mvm logs`, DO NOT LEAVE DEPRECATION NOTES, project is in development state
- [x] move `mvm vm ssh` to `mvm ssh`, DO NOT LEAVE DEPRECATION NOTES, project is in development state
- [x] change `cli/asset.py` to `cli/bin.py` to be consistent with the top command name, , DO NOT LEAVE DEPRECATION NOTES, project is in development state

# Guestfs

- [x] apply the aggressive optimization at docs/optimizatoins/guestfs_boot.md

# NEW Features

- [x] Console/serial access to VM without SSH: lets implement the interactive serial wrapper so that we can send commands to the vm. Also, The expected implementation is like this:
    - user runs `mvm console <vm-sha-id>` or --name/-n for vm name, then it connects to an interactive console-like to the vm where user can send commands and it shows back the output.
    - if user accesses the vm at the initial stage of vm creation `mvm console -n r1` then it must also output the initial streaming of the boot where firecracker exposes the serial output to this!
    - the services/ layer, we need to understand how the implementation looks like. If it's an implementation where it spawns a subprocess with its own pid, then yes lets go with services/ layer implementation. But if it does not have any pid on its own, then we will go with the same paterrn where core -> api -> cli similar with other commands.
    - **STATUS: ALREADY IMPLEMENTED** - Located in services/console_relay/, cli/console.py
