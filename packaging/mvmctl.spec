Name:           mvmctl
Version:        0.1.0
Release:        1%{?dist}
Summary:        MicroVM Manager - Container speed, VM isolation

License:        MIT
URL:            https://github.com/AlanD20/mvmctl
Source0:        https://github.com/AlanD20/mvmctl/releases/download/v%{version}/mvm
Source1:        https://raw.githubusercontent.com/AlanD20/mvmctl/v%{version}/docs/mvm.1

BuildArch:      x86_64
Requires:       iproute, iptables, qemu-img, libguestfs, xorriso, openssh-clients

%description
mvmctl is a production-grade CLI for managing microVMs on Linux.
It handles VM lifecycle: downloading kernels/images, networking, VM creation,
SSH access, log streaming, snapshots, and cleanup.

%prep
# No prep needed for binary distribution

%build
# Binary is already built

%install
install -D -m 755 %{SOURCE0} %{buildroot}/usr/bin/mvm
install -D -m 644 %{SOURCE1} %{buildroot}/usr/share/man/man1/mvm.1
gzip -9 %{buildroot}/usr/share/man/man1/mvm.1

%files
/usr/bin/mvm
/usr/share/man/man1/mvm.1.gz

%changelog
* Mon Mar 30 2026 AlanD20 <aland20@pm.me> - 0.1.0-1
- Initial RPM release
- Firecracker microVM management
- Network bridge and TAP management
- SSH key and image management
- VM lifecycle (create, start, stop, remove, snapshot)
- Distribution packages support
- Comprehensive test suite (2300+ tests)
