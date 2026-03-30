# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-03-30

### Added
- Initial Python CLI (`mvm`) for managing Firecracker microVMs
- VM lifecycle management (create, start, stop, remove, snapshot, load)
- Firecracker microVM orchestration
- Bridge/TAP networking with NAT and iptables management
- SSH key management (generate, import, set-default)
- Kernel management (fetch official and Firecracker-optimized kernels)
- Image management (fetch from registry, import local files)
- Binary management (download Firecracker releases)
- Cloud-init integration with nocloud-net server
- Console relay for VM serial access
- Log streaming (boot logs and Firecracker process logs)
- VM pruning and cache management
- Host initialization (KVM, networking, sudoers setup)
- Named network support with IP lease management
- State-based VM directory structure using SHA256 hashes
- Short ID resolution (6-character prefixes)
- Comprehensive test suite (2300+ tests, 82% coverage)
- Distribution packages (.deb, .rpm, PKGBUILD)
- Man page documentation
- Startup time compliance testing (< 200ms requirement)
- CI/CD with GitHub Actions

### Performance
- CLI startup optimized to ~140ms via typer-slim
- Lazy-loading architecture for subcommands via LazyMVMGroup
- Nuitka-compiled standalone binary available

[Unreleased]: https://github.com/AlanD20/mvmctl/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
