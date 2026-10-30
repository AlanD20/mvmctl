package image

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

func TestResumeStagedImagePreservesExactIntentAndSource(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	cache, err := infra.GetCacheDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cache, "images"), 0700))
	// The filesystem probe is external; retained identities, manifests, locks and bytes remain real.
	installVMJournalRunner(t, &vmJournalRunner{effect: func(
		context.Context, []string, system.RunCmdOpts,
	) (*system.RunResult, error) {
		return &system.RunResult{Stdout: "ext4\n"}, nil
	}})
	for name, mutate := range map[string]func(*testing.T, *stagedImage, string){
		"unchanged": func(*testing.T, *stagedImage, string) {},
		"missing manifest": func(t *testing.T, stage *stagedImage, _ string) {
			require.NoError(t, stage.root.Remove(stagedRecoveryLeaf))
		},
		"symlink manifest": func(t *testing.T, stage *stagedImage, source string) {
			require.NoError(t, stage.root.Remove(stagedRecoveryLeaf))
			require.NoError(t, stage.root.Symlink(source, stagedRecoveryLeaf))
		},
		"changed source": func(t *testing.T, _ *stagedImage, source string) {
			file, err := os.OpenFile(source, os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = file.WriteAt([]byte("changed"), 0)
			require.NoError(t, err)
			require.NoError(t, file.Close())
		},
	} {
		t.Run(name, func(t *testing.T) {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(t.Name())))
			source := filepath.Join(cache, "images", id+".img")
			file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			require.NoError(t, err)
			require.NoError(t, file.Truncate(16<<20))
			require.NoError(t, file.Close())
			stage, err := prepareStagedImage(t.Context(), source, id, "ext4")
			require.NoError(t, err)
			require.NoError(t, stage.prepareRecovery(t.Context(), "alpine", false, true))
			mutate(t, stage, source)
			require.NoError(t, stage.close(false))
			rootfs := filepath.Join(cache, "images", "staging", id, "rootfs.img")
			before, err := os.Stat(rootfs)
			require.NoError(t, err)
			sourceBefore, err := os.ReadFile(source)
			require.NoError(t, err)
			path, err := NewService(nil).ResumeStagedImagePath(t.Context(), id)
			if name == "unchanged" {
				require.NoError(t, err)
				assert.Equal(t, source, path)
				// A second admission also proves that the first one released its recovery lock.
				again, err := NewService(nil).ResumeStagedImagePath(t.Context(), id)
				require.NoError(t, err)
				assert.Equal(t, source, again)
			} else {
				require.Error(t, err)
				assert.Empty(t, path)
			}
			after, err := os.Stat(rootfs)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "resume/refusal must retain the exact staging inode")
			sourceAfter, err := os.ReadFile(source)
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(sourceBefore), sha256.Sum256(sourceAfter),
				"source bytes must not be re-extracted or replaced")
		})
	}
}
