# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-03-30

### Added
- (Add changes here)

### Changed
- (Add changes here)

### Fixed
- (Add changes here)

### Added
- `mvm vm pause` / `mvm vm resume` commands for live VM state management
- `mvm asset bin fetch` / `mvm asset bin list` / `mvm asset bin use` for Firecracker binary management
- `mvm key create` / `mvm key add` / `mvm key list` / `mvm key remove` SSH key registry
- Named network management via `mvm network create` / `mvm network remove` / `mvm network list`
- Host initialisation with privilege delegation (`mvm host init` / `mvm host reset`)
- Python API layer (`mvmctl.api.*`) for programmatic VM management without the CLI
- PyInstaller standalone binary build for Ubuntu 22.04 and 24.04
- Cloud-init support for first-boot VM configuration
- Snapshot and restore support via Firecracker API socket
- Audit logging for privileged operations

### Changed
- Replaced bash proof-of-concept scripts with production-grade Python CLI
- `mvm host init` now persists MVM iptables chains via `iptables-save` to `/etc/iptables/rules.v4`; requires `iptables-persistent` (Debian/Ubuntu) or `iptables-services` (RHEL) for automatic boot-time restore

### Breaking Changes

### Security
- NAT teardown is guarded: MASQUERADE rule only removed when no VMs are attached to the bridge
- SSH known_hosts generated per-VM when using `mvm vm ssh`

## [0.1.0] — Initial Release

### Added
- Initial Python CLI (`mvm`) for managing Firecracker microVMs
- Single-VM and multi-VM bash scripts (legacy, in `single-vm/` and `multi-vm/`)
