package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"mvmctl/internal/infra/system"
)

// BuildVersion is set at build time via ldflags: -X mvmctl/internal/app.BuildVersion=1.0.0
var BuildVersion string

// Version holds the current base version string, used for User-Agent headers
// and build metadata. Defaults to "0.1.0" (matching Python's __version__).
// When BuildVersion is set via ldflags, it takes priority.
// git info is resolved at runtime and appended via GetVersion().
var Version = "0.1.0"

// SourceDir is the source directory embedded at build time via:
//
//	-ldflags "-X mvmctl/internal/app.SourceDir=$(pwd)"
//
// When set, GetGitVersionInfo() starts its search from SourceDir instead of
// os.Getwd(), matching Python's Path(__file__).parent.parent.parent which
// always looks relative to the source file.
var SourceDir string

// getVersionOnce ensures lazy, one-time resolution of the full version (with git info).
// TODO: call InitVersion() from app/app.go explicitly
var getVersionOnce sync.Once
var getVersionCached string

// GetGitVersionInfo matches Python's _get_git_version_info() in main.py lines 61-104.
// Starts the search from SourceDir (embedded build path or runtime.Caller-derived path),
// which matches Python's Path(__file__).parent.parent.parent.
// Falls back to os.Getwd() (walking up to 5 levels) if SourceDir is not set.
// Returns:
//   - Tag name if current commit is tagged
//   - "git+<short_hash>" if not tagged
//   - empty string if not in a git repo or git not available
func GetGitVersionInfo() string {
	searchDirs := []string{}

	if SourceDir != "" {
		searchDirs = append(searchDirs, SourceDir)
	}

	// Fallback: try cwd (walking up to 5 levels)
	cwd, err := os.Getwd()
	if err == nil {
		searchDirs = append(searchDirs, cwd)
	}

	for _, dir := range searchDirs {
		for i := 0; i < 5; i++ {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				// Python: git describe --tags --exact-match HEAD
				tag, err := runGitCmd(context.Background(), dir, "describe", "--tags", "--exact-match", "HEAD")
				if err == nil && tag != "" {
					return tag
				}

				// Python: git rev-parse --short HEAD
				hash, err := runGitCmd(context.Background(), dir, "rev-parse", "--short", "HEAD")
				if err == nil && hash != "" {
					return "git+" + hash
				}

				return ""
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func runGitCmd(ctx context.Context, repoDir string, args ...string) (string, error) {
	result := system.RunCmdCompat(ctx, append([]string{"git", "-C", repoDir}, args...), system.RunCmdOptions{
		Capture: true,
		Check:   true,
	})
	if result.Err != nil {
		return "", result.Err
	}
	return strings.TrimSpace(result.Stdout), nil
}

// GetVersion implements Python's _get_version() from main.py lines 107-135.
// Resolution chain (matching Python):
//  1. BuildVersion (set via ldflags) — returned as-is (Python returns BUILD_VERSION as-is)
//  2. runtime/debug.ReadBuildInfo().Main.Version — Go module version (matches importlib.metadata)
//  3. Fallback to Version (default "0.1.0" — matches Python's __version__)
//  4. Append git info unless a tag is found (tag overrides version entirely)
//
// Result cached after first call.
func GetVersion() string {
	getVersionOnce.Do(func() {
		// — init logic (formerly in init()) —
		if BuildVersion != "" {
			Version = BuildVersion
		}
		// If SourceDir not set via ldflags, derive from runtime.Caller(0) at init time.
		// This only works during development (go run / go build) when source files are
		// accessible at their original build locations.
		if SourceDir == "" {
			if _, file, _, ok := runtime.Caller(0); ok {
				// file is .../internal/app/version.go
				// Walk up: version.go → app/ → internal/ → mvmctl/ (repo root)
				dir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
				if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
					SourceDir = dir
				}
			}
		}
		// — end init logic —

		// 1. Build-time version baked in by Makefile (takes priority).
		//    Python's _get_version() returns BUILD_VERSION as-is with NO git info appended.
		if BuildVersion != "" {
			getVersionCached = BuildVersion
			return
		}

		// 2. Try Go module version (equivalent to Python's importlib.metadata.version())
		version := getModuleVersion()
		if version == "" {
			// 3. Fallback to hardcoded version (matching Python's __version__ = "0.1.0")
			version = Version
			if version == "" {
				version = "0.1.0"
			}
		}

		// 4. Add git info if available
		//    Python: if git_info: version = f"{version}+{git_info}" if git+ else version = git_info
		gitInfo := GetGitVersionInfo()
		if gitInfo != "" {
			if strings.HasPrefix(gitInfo, "git+") {
				version = version + "+" + gitInfo
			} else {
				// It's a tag, use tag as version (Python: version = git_info)
				version = gitInfo
			}
		}

		getVersionCached = version
	})
	return getVersionCached
}

// getModuleVersion returns the Go module version, equivalent to Python's
// importlib.metadata.version(bootstrap_name).
func getModuleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	// info.Main.Version is "(devel)" for locally-built binaries;
	// only use it if it's a real semver.
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		return ""
	}
	return v
}

// VersionString returns the display version string matching Python's _get_version().
func VersionString() string {
	return GetVersion()
}
