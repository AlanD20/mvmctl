Metadata:
- the binaries>firecracker should not contain jailer_path
- the binaries>firecracker should not contain active_binary_path
- the binaries>jailer should not contain firecracker_path
- the binaries>jailer should not contain active_binary_path
- the binaries should contain defaults key where it has all the default binary paths like the following examples:
    - binaries>defaults>firecracker>binary_path, binaries>defaults>firecracker>full_version
    - binaries>defaults>jailer>binary_path, binaries>defaults>jailer>full_version

VM:
- when creating a new vm via `mvm vm create` it copies the rootfs file into the vm's state folder! the kernel and the rootfs must use the absolute path of the kernel or rootfs provided or if default is chosen, then use default's absolute path of rootfs and kernel. do not copy rootfs or kernel into each vm's state!
- firecracker rootfs requires integrating the ssh key into the image! need to figure out a way to do this? perhaps create a copy of an image by integrating a file?
- introduce --kernel-path and --image-path to `mvm vm create` to allow custom image and kernel path
- when `mvm vm create` throws an exception, it leaves out the directory creation of the vm state!
- ensure root partition detection is available on both `mvm image fetch` and `mvm image import`
- nocloud-net port from the flag is not passed dynamically in the code.
- DO NOT COPY rootfs into vm state file! use absolute file to cached imgaes
-  when user fetches/imports an image via `mvm image fetch/import`, the process at the end it must run 'blkid -p -s UUID -o value' on the final image that has only rootfs content, and then store the `fs_uuid` in the image's metadata. And then later when user enters `mvm vm create ...` the command must pull the `fs_uuid` of the image from metadata and use it as boot arg with root=UUID={fs_uuid}

Kernel:
- `mvm kernel fetch` the effective_arch defaults to the `host machine arch` (e.g., x86_64), this is wrong, it must be default to x86_64 because the CLI has defined it! this does not have relevency with current host's architecture
- user enters `mvm kernel fetch --official` then it will pull the kernel 6.19.9 official defined in kernels.yaml file! and then the config_fragmets is an array of either local path relative to assets folder or HTTP url where during the build it will apply these config files to kernel, and then later goes through enabled/disabled and set value keys to do those against the config!
- when running `mvm kernel fetch --official` triggers the cache marker, the metadata is not updated for the kernel!

CLI:
- implement progress bar when fetching kernel/image/binary
- firecracker failure and exit codes arent caught by `mvm vm ls`, it should appropriately follow and track if pid exist or exit out!
- when running `ls` on EVERY SUPPORTED commands such as `mvm image ls`, etc.. it must read through the metadata and check if it has a path, file exist? if no, add (X) mark to indicate it's deleted and only metadata left. if it's a network bridge, check if it's still there. etc.. the state check with actual environment depends on the command, for image, kernel, vm, key, bin, they are file state checks, but for network, it's a check with the actual bridge if it still exists!
- when user enters `rm` for any resources such as kernel, image, vm, keys, etc.. it shouldn't prompt y/n. It MUST PROCEED WITH REMOVAL.
- DO NOT ALLOW REMOVAL OF networks, images, kernels, if they are used by an active VM. The CLI must utilize the metadata to ensure there isnt an active VM using these.


Following UI errors must be more friendlier for user:
- Running `mvm key add ~/.ssh/id_rsa.pub`
- Running `mvm network create` when user doesn't have proper permission

Networking:
- prompt user to provide the interface that provides internet for routing


Escalation:
- handle privilege escalations exactly how `mvm network create` is handling it!

Codebase Maintainability:
- Ensure ALL 'yaml id' references in the code are replaced with internal id.
- move values from constants.py to defaults.yaml for cloud-init
- add the entire cloud-init config to --output-config with cloud-init as the key, then under it all the user-data, meta-data, network-config
- config_gen.py line 227 must come from defaults.yaml!!

