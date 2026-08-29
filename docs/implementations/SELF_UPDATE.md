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
  ├── 1. CLI checks the latest release and compares BuildVersion
  ├── 2. Apply detects the current binary path via os.Executable()
  ├── 3. Reject canonical /usr/local/bin/mvm with the administrator install instruction
  ├── 4. Fetch the release for the apply operation
  ├── 5. Find the matching asset for arch (mvm / mvm-arm64)
  ├── 6. Download checksums.sha256
  ├── 7. Download the binary to a temp file beside the user-owned artifact
  ├── 8. Verify SHA256 against checksums.sha256
  ├── 9. os.Rename(temp, current_path)
  ├── 10. Restore executable permissions
  └── 11. Print success
```

## Edge cases

### 1. Binary in a root-owned path

Package-managed `/usr/bin/mvm` remains owned by the package manager. A non-root apply fails when it cannot write the
target directory; self-update never attempts sudo. Use the package manager for these installations.

### 2. Daemon child processes running (console relay, nocloud-net)

Safe. `os.Rename()` atomically swaps the directory entry without touching the running binary's inode. All existing processes continue using the old inode. New processes use the new inode. Backward compatibility ensures daemons and the new binary coexist.

### 3. Partial download / disk full

Download to a temp file in the same directory as the target binary (same filesystem → rename is atomic). If download fails, clean up the temp file. The old binary is untouched until rename succeeds. No corruption possible.

### 4. Checksum mismatch

Delete temp file, print error with checksum details. Never rename on mismatch.

### 5. Permission restoration after rename

After rename, set `os.Chmod(newPath, 0755)` so the user-owned artifact stays executable. Sudoers never authorizes that
path; the privileged target is the separate root-owned system installation.

### 6. Root-owned binary

Self-update only replaces user-owned artifacts. The apply service rejects canonical root-owned `/usr/local/bin/mvm`
before its own release request and directs an administrator to run the new trusted artifact through
`sudo <new-mvm-binary> host install-system`. The public `self-update` and `self-update apply` commands perform their
initial update check first, so they may contact the release service before apply returns that instruction.
Package-managed `/usr/bin/mvm` installations continue to use their package manager.

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
