# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `fcm vm pause` / `fcm vm resume` commands for live VM state management
- `fcm asset bin fetch` / `fcm asset bin list` / `fcm asset bin use` for Firecracker binary management
- `fcm key create` / `fcm key add` / `fcm key list` / `fcm key remove` SSH key registry
- Named network management via `fcm network create` / `fcm network remove` / `fcm network list`
- Host initialisation with privilege delegation (`fcm host init` / `fcm host reset`)
- Python API layer (`fcm.api.*`) for programmatic VM management without the CLI
- PyInstaller standalone binary build for Ubuntu 22.04 and 24.04
- Cloud-init support for first-boot VM configuration
- Snapshot and restore support via Firecracker API socket
- Audit logging for privileged operations

### Changed
- Replaced bash proof-of-concept scripts with production-grade Python CLI

### Security
- NAT teardown is guarded: MASQUERADE rule only removed when no VMs are attached to the bridge
- SSH known_hosts generated per-VM when using `fcm vm ssh`

## [0.1.0] — Initial Release

### Added
- Initial Python CLI (`fcm`) for managing Firecracker microVMs
- Single-VM and multi-VM bash scripts (legacy, in `single-vm/` and `multi-vm/`)
