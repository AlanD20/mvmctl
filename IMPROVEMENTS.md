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


UI:
- implement progress bar when fetching kernel/image/binary


Following UI errors must be more friendlier for user:
- Running `mvm key add ~/.ssh/id_rsa.pub`
- Running `mvm network create` when user doesn't have proper permission

Networking:
- prompt user to provide the interface that provides internet for routing


Escalation:
- handle privilege escalations exactly how `mvm network create` is handling it!
