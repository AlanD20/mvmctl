When making these changes. Ensure that there will be NO DEPRECATION messages/codes are left over. This project is under active development and IS NOT READY FOR PRODUCTION YET.

Metadata:
- the binaries>firecracker should not contain jailer_path
- the binaries>firecracker should not contain active_binary_path
- the binaries>jailer should not contain firecracker_path
- the binaries>jailer should not contain active_binary_path
- the binaries should contain defaults key where it has all the default binary paths like the following examples:
    - binaries>defaults>firecracker>binary_path, binaries>defaults>firecracker>full_version
    - binaries>defaults>jailer>binary_path, binaries>defaults>jailer>full_version

cloud-init:
- cleanup_orphans function must be implemented in nocloud_net_manager.py file

VM:
- [x] when user fetches/imports an image via `mvm image fetch/import`, the process at the end it must run 'blkid -p -s UUID -o value' on the final image that has only rootfs content, and then store the `fs_uuid` in the image's metadata. And then later when user enters `mvm vm create ...` the command must pull the `fs_uuid` of the image from metadata and use it as boot arg with root=UUID={fs_uuid}
- [x] DO NOT COPY rootfs into vm state file! use absolute file to cached imgaes. when creating a new vm via `mvm vm create` it copies the rootfs file into the vm's state folder! the kernel and the rootfs must use the absolute path of the kernel or rootfs provided or if default is chosen, then use default's absolute path of rootfs and kernel. do not copy rootfs or kernel into each vm's state! -- THIS MUST BE REVERTED, the actual rootfs is the system, that means the size is also the system's size. maybe expose flags to increase the size!!
- [x] each image has `fs_type` in the metadata file, this type must be used in the boot arg of firecracker json file which `rootfstype={fs_type}`
- firecracker rootfs requires integrating the ssh key into the image! need to figure out a way to do this? perhaps create a copy of an image by integrating a file?
- introduce --kernel-path and --image-path to `mvm vm create` to allow custom image and kernel path
- when `mvm vm create` throws an exception, it leaves out the directory creation of the vm state!
- ensure root partition detection is available on both `mvm image fetch` and `mvm image import`
- nocloud-net port from the flag is not passed dynamically in the code.
- The vm state folder per vm must be changed from vm name to full sha for the folder name to make it unique even if duplicate names are being used.

Kernel:
- [x] user enters `mvm kernel fetch --official` then it will pull the kernel 6.19.9 official defined in kernels.yaml file! and then the config_fragmets is an array of either local path relative to assets folder or HTTP url where during the build it will apply these config files to kernel, and then later goes through enabled/disabled and set value keys to do those against the config!
- `mvm kernel fetch` the effective_arch defaults to the `host machine arch` (e.g., x86_64), this is wrong, it must be default to x86_64 because the CLI has defined it! this does not have relevency with current host's architecture
- when running `mvm kernel fetch --official` triggers the cache marker, the metadata is not updated for the kernel!

CLI:
- implement progress bar when fetching kernel/image/binary
- firecracker failure and exit codes arent caught by `mvm vm ls`, it should appropriately follow and track if pid exist or exit out! each vm has `firecracker.pid` in the vm folder state. use this pid to track! and ideally a way to show the exit code in the `status` field.
- when running `ls` on EVERY SUPPORTED commands such as `mvm image ls`, etc.. it must read through the metadata and check if it has a path, file exist? if no, add (X) mark to indicate it's deleted and only metadata left. if it's a network bridge, check if it's still there. etc.. the state check with actual environment depends on the command, for image, kernel, vm, key, bin, they are file state checks, but for network, it's a check with the actual bridge if it still exists!
- when user enters `rm` for any resources such as kernel, image, vm, keys, etc.. it shouldn't prompt y/n. It MUST PROCEED WITH REMOVAL.
- DO NOT ALLOW REMOVAL OF networks, images, kernels, if they are used by an active VM. The CLI must utilize the metadata to ensure there isnt an active VM using these.
- Add size to `kernel`, `image` ls commands

Debugging: Complete overhaul of every single path of the CLI application and handle any user facing error gracefully as a friendly output. DO NOT SHOW EXCEPTIONS and STACK TRACES, unless DEBUG MODE is enabled. Every single path of the entire application must support this mode, ensure that every single path of the code has this particularly more emphasis on complex logics requiring sequential state in order to allow the cli application to perform an action.
- introduce a new build type that enables debugging at finer grain for easier debugging issues. a value defined in constants.py file where it derives the DEBUG_MODE value from defaults.yaml file! this will enable debug mode throughout the cli application.
- DEBUG MODE also introduces verbosity of errors, warnings, stack traces, etc.. to improve debugging throughout the application when an error occurs.


Following UI errors must be more friendlier for user:
- Running `mvm key add ~/.ssh/id_rsa.pub`
- Running `mvm network create` when user doesn't have proper permission

Networking:
- prompt user to provide the interface that provides internet for routing
- When a new network is created, this is effectively a new bridge with its own subnet and every rule. in `chain POSTROUTING` the target for this bridge must only allow `source` for the cidr provided only! and the `out` value must be the bridge interface name! for example a new network called `mvm-test` with cidr 175.39.0.0/24. the expected `source` value is 175.39.0.0/24 and `out` value is !mvm-test when the target is `MVM-POSTROUTING` chain.
- when `--ip` is passed to `mvm vm create` the IP isnt being checked against if this ip is already leased or free. the application must show a friendly error that given ip on given network name is already reserved.
- Explore fully isolated bridge networking mechanism for vms.


Escalation:
- handle privilege escalations exactly how `mvm network create` is handling it!

Codebase Maintainability:
- Ensure ALL 'yaml id' references in the code are replaced with internal id.
- move values from constants.py to defaults.yaml for cloud-init
- add the entire cloud-init config to --output-config with cloud-init as the key, then under it all the user-data, meta-data, network-config
- config_gen.py line 227 must come from defaults.yaml!!
- move `mvm vm logs` to `mvm logs`, DO NOT LEAVE DEPRECATION NOTES, project is in development state
- move `mvm vm ssh` to `mvm ssh`, DO NOT LEAVE DEPRECATION NOTES, project is in development state
- change `cli/asset.py` to `cli/bin.py` to be consistent with the top command name, , DO NOT LEAVE DEPRECATION NOTES, project is in development state


NEW Features:
- Console/serial access to VM without SSH: lets implement the interactive serial wrapper so that we can send commands to the vm. Also, The expected implementation is like this:
    - user runs `mvm console <vm-sha-id>` or --name/-n for vm name, then it connects to an interactive console-like to the vm where user can send commands and it shows back the output.
    - if user accesses the vm at the initial stage of vm creation `mvm console -n r1` then it must also output the initial streaming of the boot where firecracker exposes the serial output to this!
    - the services/ layer, we need to understand how the implementation looks like. If it's an implementation where it spawns a subprocess with its own pid, then yes lets go with services/ layer implementation. But if it does not have any pid on its own, then we will go with the same paterrn where core -> api -> cli similar with other commands.
    - Add `mvm vm inspect <id|--name|-n` to view the metadata of the target vm

