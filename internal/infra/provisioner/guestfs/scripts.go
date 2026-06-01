package guestfs

import "text/template"

// ── Template data structs ────────────────────────────────────────────────────

type hostnameData struct {
	Hostname string
}

type dnsData struct {
	DNSServer string
}

type userData struct {
	User    string
	UserUID int
	UserGID int
}

type sshKeysData struct {
	User string
	Home string
	Keys []string
}

// ── Shell script templates ──────────────────────────────────────────────────
//
// Each template produces a shell command that runs INSIDE the guest via
// guestfish `sh "..."`. All conditionals use the guest's own tools.

var setHostnameTmpl = template.Must(template.New("set_hostname").Parse(
	`sed -i '/^127\.0\.1\.1/d' /etc/hosts
printf '127.0.1.1\t{{.Hostname}}\n' >> /etc/hosts
printf '%s\n' '{{.Hostname}}' > /etc/hostname`,
))

var injectDNSTmpl = template.Must(template.New("inject_dns").Parse(
	`grep -qs '^nameserver' /etc/resolv.conf 2>/dev/null ||
  printf 'nameserver {{.DNSServer}}\n' > /etc/resolv.conf`,
))

var ensureUserTmpl = template.Must(template.New("ensure_user").Parse(
	`if ! id '{{.User}}' 2>/dev/null; then
  useradd -m -u {{.UserUID}} -U -s /bin/bash '{{.User}}'
  mkdir -p "/home/{{.User}}/.ssh"
  chown {{.UserUID}}:{{.UserGID}} "/home/{{.User}}/.ssh"
  printf '%s ALL=(ALL) NOPASSWD: ALL\n' '{{.User}}' > "/etc/sudoers.d/{{.User}}"
  chmod 440 "/etc/sudoers.d/{{.User}}"
fi`,
))

var addSSHKeysTmpl = template.Must(template.New("add_ssh_keys").Parse(
	`mkdir -p "{{.Home}}/.ssh"
{{range $k := .Keys}}
if ! grep -qF '{{$k}}' "{{$.Home}}/.ssh/authorized_keys" 2>/dev/null; then
  printf '%s\n' '{{$k}}' >> "{{$.Home}}/.ssh/authorized_keys"
fi
{{end}}
chmod 0700 "{{.Home}}/.ssh"
chmod 0600 "{{.Home}}/.ssh/authorized_keys"
chown -R {{.User}}:{{.User}} "{{.Home}}/.ssh" 2>/dev/null || true`,
))

var generateHostKeysTmpl = template.Must(template.New("generate_host_keys").Parse(
	`all_exist=true
for key in ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key; do
  [ -f "/etc/ssh/$key" ] || { all_exist=false; break; }
done
$all_exist && exit 0
mkdir -p /etc/local.d /etc/systemd/system /etc/systemd/system/multi-user.target.wants
cat > /etc/local.d/ssh-keygen.start << 'TMVM'
#!/bin/sh
rm -f /etc/ssh/ssh_host_*
ssh-keygen -A
rm -f /etc/ssh/ssh_host_*_key.pub
chmod 0600 /etc/ssh/ssh_host_*_key
TMVM
chmod 0755 /etc/local.d/ssh-keygen.start
cat > /etc/systemd/system/ssh-hostkeygen.service << 'TMVM'
[Unit]
Description=SSH Host Key Generator
Before=ssh.service sshd.service
ConditionPathExistsGlob=/etc/ssh/ssh_host_*_key
[Service]
Type=oneshot
ExecStart=/usr/bin/ssh-keygen -A
[Install]
WantedBy=multi-user.target
TMVM
ln -sf /etc/systemd/system/ssh-hostkeygen.service /etc/systemd/system/multi-user.target.wants/ssh-hostkeygen.service 2>/dev/null || true
if [ -f /sbin/openrc ] || [ -f /usr/sbin/openrc ]; then
  mkdir -p /etc/runlevels/default
  ln -sf /sbin/openrc-local /etc/runlevels/default/local 2>/dev/null || true
fi`,
))

var enableSSHTmpl = template.Must(template.New("enable_ssh").Parse(
	`# Enable SSH via systemd
for unit in ssh sshd; do
  for dir in /usr/lib/systemd/system /lib/systemd/system /etc/systemd/system; do
    if [ -f "$dir/$unit.service" ]; then
      mkdir -p /etc/systemd/system/multi-user.target.wants
      ln -sf "$dir/$unit.service" "/etc/systemd/system/multi-user.target.wants/$unit.service" 2>/dev/null || true
      break 2
    fi
  done
done
# Enable SSH via OpenRC
if [ -f /sbin/openrc ] || [ -f /usr/sbin/openrc ]; then
  mkdir -p /etc/runlevels/default
  [ -f /etc/init.d/sshd ] && ln -sf /etc/init.d/sshd /etc/runlevels/default/sshd 2>/dev/null || true
  [ -f /etc/init.d/ssh ] && ln -sf /etc/init.d/ssh /etc/runlevels/default/ssh 2>/dev/null || true
fi
# Enable SSH via sysvinit
if [ -f /etc/init.d/ssh ]; then
  for level in 2 3 4 5; do
    mkdir -p "/etc/rc$level.d"
    [ ! -e "/etc/rc$level.d/S02ssh" ] && ln -sf ../init.d/ssh "/etc/rc$level.d/S02ssh" 2>/dev/null || true
  done
fi
mkdir -p /etc/ssh/sshd_config.d
chmod 0755 /etc/ssh/sshd_config.d`,
))

var disableCloudInitTmpl = template.Must(template.New("disable_cloud_init").Parse(
	`mkdir -p /etc/cloud/cloud.cfg.d
printf 'datasource_list: [None]\n' > /etc/cloud/cloud.cfg.d/99-disable-datasources.cfg
printf 'disabled by mvmctl\n' > /etc/cloud-init.disabled
mkdir -p /etc/systemd/system/snapd.seeded.service.d
printf '[Service]\nExecStart=\nExecStart=/bin/true\n' > /etc/systemd/system/snapd.seeded.service.d/override.conf
mkdir -p /etc/systemd/system/systemd-networkd-wait-online.service.d
printf '[Unit]\nConditionPathExists=/nonexistent-disabled-by-mvm\n' > /etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf
for svc in cloud-init cloud-init-local cloud-config cloud-final; do
  ln -sf /dev/null "/etc/systemd/system/$svc.service" 2>/dev/null || true
done`,
))

var deblobTmpl = template.Must(template.New("deblob").Parse(
	`# ── Common cleanup ──
rm -rf /var/log/* /tmp/* /var/tmp/* 2>/dev/null || true
rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/* 2>/dev/null || true
find /var/log -type f -delete 2>/dev/null || true
# ── OS-specific cache cleanup ──
case "$(grep ^ID= /etc/os-release 2>/dev/null | cut -d= -f2 | tr -d '"')" in
  alpine)
    apk cache clean 2>/dev/null || true
    rm -rf /var/cache/apk/* 2>/dev/null || true
    ;;
  ubuntu|debian)
    apt-get clean 2>/dev/null || true
    rm -rf /var/cache/apt/archives/*.deb 2>/dev/null || true
    rm -rf /var/cache/debconf/* 2>/dev/null || true
    ;;
esac
# ── Fix fstab (PARTUUID → /dev/vda) ──
if [ -f /etc/fstab ]; then
  sed -i 's/^PARTUUID=[^[:space:]]*/\/dev\/vda/' /etc/fstab 2>/dev/null || true
fi
# ── Mask slow services ──
for svc in \
  systemd-resolved systemd-networkd-wait-online \
  systemd-journal-flush systemd-tmpfiles-setup \
  lvm2-monitor mdmonitor multipathd \
  haveged snapd snapd.seeded; do
  ln -sf /dev/null "/etc/systemd/system/$svc.service" 2>/dev/null || true
done`,
))


