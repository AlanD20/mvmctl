# 0008 — Image type + version listing via dynamic version resolvers

Images are moving from a flat id-based catalog (`ubuntu-24.04`, `alpine-3.21`) to a type+version model where users pull by image type and optionally select a version: `mvm image pull ubuntu --version 24.04`. This avoids hardcoding slug naming conventions across the CLI and enables dynamic discovery of available versions from upstream providers.

## What changed

- `images.yaml` was restructured from a flat `images:` list to a grouped `image_types:` array. Each type defines a `versions_url`, a `resolver` (currently `http-dir` or `firecracker-s3`), and options for parsing directory listings (`codename_mapping`, `version_prefix`, `skip_patterns`).
- A single `HttpDirVersionResolver` handles Apache-style HTML directory listings for Ubuntu, Ubuntu Minimal, Debian, and Alpine — all use the same HTML `<a href>` pattern. The differences are fully expressed in YAML config, not code.
- Firecracker CI keeps its existing S3 bucket listing (`list_url_template`), which is fundamentally different from HTTP directory listing.
- Arch Linux has no version listing — it's a single rolling release URL.
- A new `ImageVersion` dataclass is the uniform return type from all resolvers: `{version, codename, type, download_url, sha256_url, format}`.
- Version listings are cached for 1 hour via the existing HTTP caching infrastructure. A `--no-cache` flag forces a live fetch.
- `mvm image ls --remote` shows a grouped tree view (`type → versions`), defaulting to 5 versions per type.
- Backward compatibility is NOT maintained pre-v1 — existing image IDs like `ubuntu-24.04` still work as exact slugs, but the primary CLI interface is now `pull <type> [--version <ver>]`.

## Why not per-provider resolvers

Initially we considered a separate resolver class per provider (e.g., `UbuntuStreamsResolver`, `AlpineDirResolver`, `DebianDirResolver`). While exploring the actual provider APIs we discovered every supported provider (Ubuntu, Ubuntu Minimal, Debian, Alpine) uses Apache HTML directory listings with `<a href>` tags — the same format. The differences are only in directory naming conventions (version numbers vs codenames, prefix stripping), which are purely data-driven. A single `http-dir` resolver with YAML config eliminates code duplication without loss of expressiveness.

## Considered alternatives

- **`codename_mapping` in Python code, not YAML.** Rejected because codename-to-version mappings change rarely (every 2 years for Debian/Ubuntu) and are inherently data, not logic. Putting them in YAML means updates don't require code changes.
- **JSON Streams API for Ubuntu instead of HTML directory listing.** The streams API exists and is machine-readable, but it serves both Ubuntu and Ubuntu Minimal in one JSON document. The HTML directory listing is simpler to parse and already covers both types with the same `http-dir` resolver.
- **No version listing — keep flat slug-based pulls forever.** Rejected because the (type, version) interface is more intuitive and enables future features like `--version latest`.

## Consequences

- Adding a new image type requires only YAML changes unless the provider uses a non-HTTP-listing format.
- `ImageService.get_specs_for()` and its callers need updating to support the new resolution path.
- `ImageVersion` is the canonical representation of a downloadable image. Both `ImageSpec` (the config-backed model, used for image type definitions in YAML) and `ImageVersion` (returned by version resolvers) remain in active, coexisting use — the planned phase-out of `ImageSpec` has not been completed.
- Alpine needs a secondary fetch to discover the full patch version from the `releases/cloud/` directory listing (the directory is `v3.21` but filenames contain `3.21.4`). This is handled transparently inside `HttpDirVersionResolver`.
