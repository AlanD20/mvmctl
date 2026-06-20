Name:           mvmctl
Version:        0.1.0
Release:        1%{?dist}
Summary:        MicroVM Manager - Container speed, VM isolation

License:        MIT
URL:            https://github.com/AlanD20/mvmctl
Source0:        https://github.com/AlanD20/mvmctl/releases/download/v%{version}/mvm
Source1:        https://raw.githubusercontent.com/AlanD20/mvmctl/v%{version}/docs/mvm.1

# BuildArch is auto-detected from build host; supports x86_64 and aarch64.
# For multi-arch release, build the RPM on each target architecture.

Requires:       iproute, iptables, nftables, qemu-img, openssh-clients, e2fsprogs, util-linux, shadow-utils, sudo, procps-ng, kmod, tar
Recommends:     cloud-utils, libguestfs

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

%post
/usr/sbin/mandb >/dev/null 2>&1 || :

%postun
/usr/sbin/mandb >/dev/null 2>&1 || :

%files
%license LICENSE
/usr/bin/mvm
%{_mandir}/man1/mvm.1.gz

%changelog
