// Package architecture contains repository-wide static architecture checks.
package architecture

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

const repositoryModule = "mvmctl"

// Rationale: core-domain isolation is stricter than Go's cycle detection, so every production source file must be
// checked even when a one-way sibling import would compile.
func TestProductionCoreDomainsDoNotImportSiblingDomains(t *testing.T) {
	t.Parallel()

	violations, err := findCoreDomainImportViolations(repositoryRoot(t), repositoryModule)
	require.NoError(t, err)
	if diff := cmp.Diff([]coreImportViolation(nil), violations); diff != "" {
		t.Errorf("sibling core-domain imports (-want +got):\n%s", diff)
	}
}

func TestFindCoreDomainImportViolationsAllowsDomainSafeImports(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeCoreImportFixture(t, root, map[string]string{
		"internal/core/vm/same_domain.go": `package vm

import _ "mvmctl/internal/core/vm/internal/helper"
`,
		"internal/core/vm/subpackage/same_domain.go": `package subpackage

import (
	_ "mvmctl/internal/core/vm"
	_ "mvmctl/internal/lib/model"
)
`,
		"internal/core/vm/sibling_test.go": `package vm

import _ "mvmctl/internal/core/network"
`,
	})

	violations, err := findCoreDomainImportViolations(root, repositoryModule)
	require.NoError(t, err)
	if diff := cmp.Diff([]coreImportViolation(nil), violations); diff != "" {
		t.Errorf("allowed core-domain imports (-want +got):\n%s", diff)
	}
}

func TestFindCoreDomainImportViolationsRejectsSiblingImportsInRepositoryOrder(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeCoreImportFixture(t, root, map[string]string{
		"internal/core/vm/z_last.go": `package vm

import _ "mvmctl/internal/core/network"
`,
		"internal/core/network/a_first.go": `package network

import _ "mvmctl/internal/core/vm/internal/helper"
`,
	})

	violations, err := findCoreDomainImportViolations(root, repositoryModule)
	require.NoError(t, err)
	want := []coreImportViolation{
		{
			File:            "internal/core/network/a_first.go",
			Line:            3,
			ImportingDomain: "network",
			ImportedDomain:  "vm",
			ImportPath:      "mvmctl/internal/core/vm/internal/helper",
		},
		{
			File:            "internal/core/vm/z_last.go",
			Line:            3,
			ImportingDomain: "vm",
			ImportedDomain:  "network",
			ImportPath:      "mvmctl/internal/core/network",
		},
	}
	if diff := cmp.Diff(want, violations); diff != "" {
		t.Errorf("sibling core-domain imports (-want +got):\n%s", diff)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

// writeCoreImportFixture creates repository-shaped Go sources for architecture-check regression tests.
func writeCoreImportFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()

	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	}
}
