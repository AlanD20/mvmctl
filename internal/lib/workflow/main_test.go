package workflow

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps workflow state-removal tests away from the operator's actual cache.
func TestMain(m *testing.M) {
	cacheDir, err := os.MkdirTemp("", "mvm-workflow-test-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("MVM_CACHE_DIR", cacheDir); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(cacheDir)
		os.Exit(1)
	}

	code := m.Run()
	if err := os.RemoveAll(cacheDir); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
