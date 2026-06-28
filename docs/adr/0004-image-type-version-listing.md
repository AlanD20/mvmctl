# Image Type + Version Listing via Dynamic Version Resolvers

**Status:** Active
**Date:** 2026-05-22
**Last Updated:** 2026-06-20 (Go implementation complete)

Images are moving from a flat id-based catalog (`ubuntu-24.04`, `alpine-3.21`) to a type+version model where users pull by image type and optionally select a version: `mvm image pull ubuntu --version 24.04`. This avoids hardcoding slug naming conventions across the CLI and enables dynamic discovery of available versions from upstream providers.

**Table of Contents**

- [What changed](#what-changed)
- [Why Not Per-Provider Resolvers](#why-not-per-provider-resolvers)
- [Considered alternatives](#considered-alternatives)
- [Consequences](#consequences)
- [Related Decisions](#related-decisions)

## What changed

- `images.yaml` (`internal/assets/images.yaml`) was restructured from a flat `images:` list to a grouped `image_types:` array. Each type defines a `versions_url`, a `resolver` (currently `http-dir` or `firecracker-s3`), and options for parsing directory listings (`codename_mapping`, `version_prefix`, `skip_patterns`).
- A single `HttpDirVersionResolver` (in `internal/lib/download/version.go`) handles Apache-style HTML directory listings for Ubuntu, Ubuntu Minimal, Debian, and Alpine — all use the same HTML `<a href>` pattern. The differences are fully expressed in YAML config, not code.
- Firecracker CI keeps its existing S3 bucket listing (`list_url_template`), which is fundamentally different from HTTP directory listing.
- Arch Linux has no version listing — it's a single rolling release URL.
- A new `model.VersionInfo` struct (`internal/lib/model/version.go`) is the uniform return type from all resolvers:

    ```go
    type VersionInfo struct {
        Version     string `json:"version"`
        DownloadURL string `json:"download_url"`
        SHA256URL   string `json:"sha256_url,omitempty"`
        DisplayName string `json:"display_name"`
        Type        string `json:"type"`
        Format      string `json:"format"`
        Name        string `json:"name,omitempty"`
        IsPresent   bool   `json:"is_present,omitempty"`
    }
    ```

- Version listings are cached for 1 hour via the existing HTTP caching infrastructure (`HttpDiskCache` in `internal/lib/download/`). A `--no-cache` flag forces a live fetch.
- `mvm image ls -r` (or `mvm image ls --remote`) shows a flat listing of available remote versions, rendered via `common.RenderVersionTree()`. For local images, `mvm image ls` uses a table format.
- Backward compatibility is NOT maintained pre-v1 — existing image IDs like `ubuntu-24.04` still work as exact slugs, but the primary CLI interface is now `pull <type> [--version <ver>]`.

## Why Not Per-Provider Resolvers

Initially we considered a separate resolver class per provider (e.g., `UbuntuStreamsResolver`, `AlpineDirResolver`, `DebianDirResolver`). While exploring the actual provider APIs we discovered every supported provider (Ubuntu, Ubuntu Minimal, Debian, Alpine) uses Apache HTML directory listings with `<a href>` tags — the same format. The differences are only in directory naming conventions (version numbers vs codenames, prefix stripping), which are purely data-driven. A single `http-dir` resolver with YAML config eliminates code duplication without loss of expressiveness.

## Considered alternatives

- **`codename_mapping` in Go code, not YAML.** Rejected because codename-to-version mappings change rarely (every 2 years for Debian/Ubuntu) and are inherently data, not logic. Putting them in YAML means updates don't require code changes.
- **JSON Streams API for Ubuntu instead of HTML directory listing.** The streams API exists and is machine-readable, but it serves both Ubuntu and Ubuntu Minimal in one JSON document. The HTML directory listing is simpler to parse and already covers both types with the same `http-dir` resolver.
- **No version listing — keep flat slug-based pulls forever.** Rejected because the (type, version) interface is more intuitive and enables future features like `--version latest`.

## Consequences

- Adding a new image type requires only YAML changes unless the provider uses a non-HTTP-listing format.
- `GetSpecsFor()` and its callers (`internal/core/image/service.go`, line 492) were updated to support the new resolution path (two-phase: fast-path for explicit versions, then version resolver via `ResolveVersions()` in `internal/core/image/version_resolver.go`).
- `model.VersionInfo` is the canonical representation of a downloadable image. Both `ImageSpec` (the config-backed model, used for image type definitions in YAML) and `VersionInfo` (returned by version resolvers) remain in active, coexisting use — the planned phase-out of `ImageSpec` has not been completed.
- Alpine needs a secondary fetch to discover the full patch version from the `releases/cloud/` directory listing (the directory is `v3.21` but filenames contain `3.21.4`). This is handled transparently inside `HttpDirVersionResolver`.

## Related Decisions

- `internal/lib/version/resolver.go` — The `version.ParseSpec()` and `version.Resolve()` functions handle version parsing and matching for all domains. These are consumed by the image service's `ResolveVersion()` method (`internal/core/image/service.go`, line 1311).
