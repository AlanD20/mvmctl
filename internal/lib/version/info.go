// Package version provides version resolution and git information for mvmctl.
package version

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mvmctl/internal/lib/system"
)

// BuildVersion is set at build time via ldflags: -X mvmctl/internal/infra/version.BuildVersion=1.0.0
var BuildVersion string

// SourceDir is the source directory embedded at build time via:
//
//	-ldflags "-X mvmctl/internal/infra/version.SourceDir=$(pwd)"
//
// When set, GetGitVersionInfo() starts its search from SourceDir instead of
// os.Getwd(), matching Python's Path(__file__).parent.parent.parent which
// always looks relative to the source file.
var SourceDir string

var versionOnce sync.Once
var versionCached string

// VersionString returns the current base version string.
// Defaults to "0.0.0". Override via SetBuildVersion.
var versionString = "0.0.0"

// SetBuildVersion sets the build version from app startup.
// Called from app.Run() via download.SetUserAgent().
func SetBuildVersion(v string) {
	if v != "" {
		versionString = v
	}
}

// VersionString returns the display version.
func VersionString() string {
	return versionString
}

// GetGitVersionInfo matches Python's _get_git_version_info() in main.py lines 61-104.
// Starts the search from SourceDir (embedded build path) or os.Getwd().
// Returns:
//   - Tag name if current commit is tagged
//   - "git+<short_hash>" if not tagged
//   - empty string if not in a git repo or git not available
func GetGitVersionInfo(ctx context.Context) string {
	searchDirs := []string{}
	if SourceDir != "" {
		searchDirs = append(searchDirs, SourceDir)
	}

	cwd, err := os.Getwd()
	if err == nil {
		searchDirs = append(searchDirs, cwd)
	}

	for _, dir := range searchDirs {
		for range 5 {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				repoDir := dir

				tag, err := runGitCmd(ctx, repoDir, "describe", "--tags", "--exact-match", "HEAD")
				if err == nil && tag != "" {
					return strings.TrimSpace(tag)
				}

				hash, err := runGitCmd(ctx, repoDir, "rev-parse", "--short", "HEAD")
				if err == nil && hash != "" {
					return "git+" + strings.TrimSpace(hash)
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
	result, err := system.DefaultRunner.Run(
		ctx,
		append([]string{"git", "-C", repoDir}, args...),
		system.RunCmdOpts{Capture: true},
	)
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

// FormatVersion produces the display version string matching Python _get_version().
// Resolution chain:
//  1. chosenVersion (set via ldflags) — returned as-is
//  2. If empty, use "0.1.0" as hardcoded fallback.
//  3. Append git info unless a tag is found (tag overrides version entirely).
func FormatVersion(ctx context.Context, chosenVersion string) string {
	if chosenVersion != "" {
		return chosenVersion
	}

	version := "0.1.0"

	gitInfo := GetGitVersionInfo(ctx)
	if gitInfo != "" {
		if strings.HasPrefix(gitInfo, "git+") {
			version = version + "+" + gitInfo
		} else {
			version = gitInfo
		}
	}

	return version
}

// GetVersion returns a cached full version string with git info.
// Matches Python's _get_version().
func GetVersion(ctx context.Context) string {
	versionOnce.Do(func() {
		if BuildVersion != "" {
			versionCached = BuildVersion
			return
		}

		gitInfo := GetGitVersionInfo(ctx)
		version := "0.1.0"

		if gitInfo != "" {
			if strings.HasPrefix(gitInfo, "git+") {
				version = version + "+" + gitInfo
			} else {
				version = gitInfo
			}
		}

		versionCached = version
	})
	return versionCached
}
