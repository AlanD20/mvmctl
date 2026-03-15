#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

setup_network() {
  echo "Setting up network for Firecracker VM..."

  if ip link show "$TAP_DEV" &>/dev/null; then
    echo "Tap device $TAP_DEV already exists, removing..."
    ip link del "$TAP_DEV" 2>/dev/null || true
  fi

  echo "Creating tap device $TAP_DEV..."
  ip tuntap add dev "$TAP_DEV" mode tap
  ip link set dev "$TAP_DEV" up

  echo "Configuring IP addresses..."
  ip addr add "${HOST_IP}/30" dev "$TAP_DEV"

  echo "Enabling proxy ARP..."
  sysctl -w net.ipv4.conf.${TAP_DEV}.proxy_arp=1 >/dev/null

  echo "Enabling IP forwarding..."
  sysctl -w net.ipv4.ip_forward=1 >/dev/null

  echo "Setting up NAT..."
  iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
  iptables -A FORWARD -i "$TAP_DEV" -o eth0 -j ACCEPT
  iptables -A FORWARD -i eth0 -o "$TAP_DEV" -j ACCEPT

  echo "Network setup complete"
  echo "  Tap device: $TAP_DEV"
  echo "  Guest IP: $GUEST_IP"
  echo "  Host IP: $HOST_IP"
}

if [ "$1" = "check" ]; then
  if ip link show "$TAP_DEV" &>/dev/null; then
    echo "Network is already configured"
    exit 0
  else
    echo "Network not configured"
    exit 1
  fi
else
  setup_network
fi
