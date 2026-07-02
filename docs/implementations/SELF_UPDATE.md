# Self-Update — `mvm self-update`

## CLI

```
mvm self-update            # check + apply if newer
mvm self-update check      # check only, print available version
mvm self-update apply      # force apply (even if same version)
```

## Prerequisites — Shared Git Remote client

Add a reusable `Remote` struct to `internal/lib/download/` that can fetch releases from any Git forge.

```go
type Release struct {
    TagName string  `json:"tag_name"`
    Assets  []Asset `json:"assets"`
}

type Asset struct {
    Name string `json:"name"`
    URL  string `json:"browser_download_url"`
}

type Remote struct {
    BaseURL string
    Token   string
    dl      *Downloader
}

func NewGitHub(repo string) *Remote
func (r *Remote) LatestRelease(ctx context.Context) (*Release, error)
func (r *Remote) Release(ctx context.Context, tag string) (*Release, error)
```

## Flow

```
mvm self-update apply
  │
  ├── 1. Detect current binary path via os.Executable()
  ├── 2. Check write permission on the directory
  ├── 3. Fetch latest release from GitHub API
  ├── 4. Compare tag vs current BuildVersion (semver)
  ├── 5. Find matching asset for arch (mvm / mvm-arm64)
  ├── 6. Download checksums.sha256
  ├── 7. Download binary to temp file alongside current binary
  ├── 8. Verify SHA256 against checksums.sha256
  ├── 9. os.Rename(temp, current_path)
  ├── 10. Restore permissions (executable bit, ownership)
  └── 11. Print success
```

## Edge cases

### 1. Binary in a root-owned path

`os.Rename` fails if the current binary is in `/usr/bin/mvm` (package install) and the user isn't root. Fall back to printing manual install instructions with the download URL. Do not attempt sudo.

### 2. Daemon child processes running (console relay, nocloud-net)

Safe. `os.Rename()` atomically swaps the directory entry without touching the running binary's inode. All existing processes continue using the old inode. New processes use the new inode. Backward compatibility ensures daemons and the new binary coexist.

### 3. Partial download / disk full

Download to a temp file in the same directory as the target binary (same filesystem → rename is atomic). If download fails, clean up the temp file. The old binary is untouched until rename succeeds. No corruption possible.

### 4. Checksum mismatch

Delete temp file, print error with checksum details. Never rename on mismatch.

### 5. Permission restoration after rename

After rename, set `os.Chmod(newPath, 0755)` to ensure the binary stays executable. Sudoers references the path, not the inode — no change needed there.

### 6. Root-owned binary

Self-update only works for user-installed binaries (e.g. `~/.local/bin/mvm`). For package-managed installs (`/usr/bin/mvm`), print a message directing the user to use the package manager.

### 7. Same version reinstall

`mvm self-update` (no subcommand) prints "Already up to date" and exits 0. `mvm self-update apply` checks version: if same, skip unless `--force`.

## Security

- Checksum verification via `checksums.sha256` from the release
- HTTPS for all downloads
- No execution of untrusted code — binary is verified before swap
- Current binary path detected via `os.Executable()`
- No sudo escalation — if the binary path is not writable, fail with manual instructions
- GITHUB_TOKEN env var supported for authenticated requests (higher rate limit)

## Implementation plan

| Step | File | Change |
|---|---|---|
| 1 | `internal/lib/download/remote.go` | New `Remote` struct, `NewGitHub()`, `LatestRelease()`, `Release()` |
| 2 | `internal/core/binary/service.go` | Refactor to use `download.NewGitHub()` instead of inline API call |
| 3 | `internal/core/binary/utils.go` | Remove `githubRelease` struct, `mapGitHubAPIError()` |
| 4 | `internal/cli/self_update.go` | New CLI command `mvm self-update {check,apply}` |
| 5 | `internal/core/update/service.go` | Version check, download, verify, swap logic |
