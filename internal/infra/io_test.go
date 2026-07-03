package infra_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// --- OpenNoFollow ---
// Rationale: OpenNoFollow is the foundation for all safe file reads. A symlink
// bypass would enable TOCTOU race attacks on config and key files.

func TestOpenNoFollow(t *testing.T) {
	t.Run("opens_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		require.NoError(t, os.WriteFile(path, []byte("data"), 0644))

		f, err := infra.OpenNoFollow(path)
		require.NoError(t, err)
		f.Close()
	})

	t.Run("nonexistent_file_errors", func(t *testing.T) {
		_, err := infra.OpenNoFollow(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Error(t, err)
	})

	t.Run("returns_readable_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0644))

		f, err := infra.OpenNoFollow(path)
		require.NoError(t, err)
		defer f.Close()

		buf := make([]byte, 1)
		n, _ := f.Read(buf)
		assert.Equal(t, 1, n)
		assert.Equal(t, byte('x'), buf[0])
	})

	t.Run("symlink_rejected_with_eloop", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.txt")
		link := filepath.Join(dir, "link.txt")
		require.NoError(t, os.WriteFile(target, []byte("secret"), 0644))
		require.NoError(t, os.Symlink(target, link))

		_, err := infra.OpenNoFollow(link)
		assert.Error(t, err, "O_NOFOLLOW must reject symlinks")
	})
}

// --- ReadRaw ---
// Rationale: ReadRaw reads files with O_NOFOLLOW protection. Must fail on
// symlinks to prevent traversal attacks.

func TestReadRaw(t *testing.T) {
	t.Run("reads_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "data.txt")
		require.NoError(t, os.WriteFile(path, []byte("hello world"), 0644))

		got, err := infra.ReadRaw(path)
		require.NoError(t, err)
		assert.Equal(t, "hello world", got)
	})

	t.Run("empty_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.txt")
		require.NoError(t, os.WriteFile(path, []byte{}, 0644))

		got, err := infra.ReadRaw(path)
		require.NoError(t, err)
		assert.Equal(t, "", got)
	})

	t.Run("multiline_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multi.txt")
		require.NoError(t, os.WriteFile(path, []byte("line1\nline2\n"), 0644))

		got, err := infra.ReadRaw(path)
		require.NoError(t, err)
		assert.Equal(t, "line1\nline2\n", got)
	})

	t.Run("nonexistent_file_errors", func(t *testing.T) {
		_, err := infra.ReadRaw(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Error(t, err)
	})
}

// --- ReadFile ---
// Rationale: Simple os.ReadFile wrapper used throughout the codebase.

func TestReadFile(t *testing.T) {
	t.Run("reads_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "data.txt")
		require.NoError(t, os.WriteFile(path, []byte("content"), 0644))

		got, err := infra.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "content", got)
	})

	t.Run("nonexistent_errors", func(t *testing.T) {
		_, err := infra.ReadFile(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Error(t, err)
	})
}

// --- ReadYAML ---
// Rationale: ReadYAML parses YAML files with O_NOFOLLOW protection. Used for
// loading bundled YAML configs and user-provided YAML files.

func TestReadYAML(t *testing.T) {
	t.Run("reads_yaml_map", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		content := []byte("key: value\nnum: 42\n")
		require.NoError(t, os.WriteFile(path, content, 0644))

		got, err := infra.ReadYAML(path)
		require.NoError(t, err)

		m, ok := got.(map[string]any)
		assert.True(t, ok, "expected map, got %T", got)
		assert.Equal(t, "value", m["key"])
		assert.Equal(t, 42, m["num"])
	})

	t.Run("empty_file_returns_empty_map", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.yaml")
		require.NoError(t, os.WriteFile(path, []byte{}, 0644))

		got, err := infra.ReadYAML(path)
		require.NoError(t, err)
		_, ok := got.(map[string]any)
		assert.True(t, ok, "empty file should return map, got %T", got)
	})

	t.Run("nonexistent_errors", func(t *testing.T) {
		_, err := infra.ReadYAML(filepath.Join(t.TempDir(), "nonexistent.yaml"))
		assert.Error(t, err)
	})

	t.Run("invalid_yaml_errors", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		require.NoError(t, os.WriteFile(path, []byte("{invalid: [yaml}"), 0644))

		_, err := infra.ReadYAML(path)
		assert.Error(t, err)
	})
}

// --- WriteJSON / ReadJSON ---
// Rationale: JSON read/write with O_NOFOLLOW protection. Used for config files
// and structured data persistence.

func TestWriteJSON(t *testing.T) {
	t.Run("writes_indented_json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "out.json")
		data := map[string]any{"name": "test", "value": 42}

		err := infra.WriteJSON(path, data)
		require.NoError(t, err)

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"name": "test"`)
		assert.Contains(t, string(content), `  `) // indented
	})

	t.Run("overwrites_existing", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "out.json")
		require.NoError(t, infra.WriteJSON(path, map[string]any{"first": true}))
		require.NoError(t, infra.WriteJSON(path, map[string]any{"second": true}))

		var result map[string]any
		require.NoError(t, infra.ReadJSON(path, &result))
		_, hasFirst := result["first"]
		_, hasSecond := result["second"]
		assert.False(t, hasFirst)
		assert.True(t, hasSecond)
	})
}

func TestReadJSON(t *testing.T) {
	t.Run("reads_valid_json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "data.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"name":"test","value":42}`), 0644))

		var result map[string]any
		err := infra.ReadJSON(path, &result)
		require.NoError(t, err)
		assert.Equal(t, "test", result["name"])
		assert.Equal(t, float64(42), result["value"])
	})

	t.Run("nonexistent_errors", func(t *testing.T) {
		var v any
		err := infra.ReadJSON(filepath.Join(t.TempDir(), "nonexistent.json"), &v)
		assert.Error(t, err)
	})

	t.Run("invalid_json_errors", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte(`not json`), 0644))

		var v any
		err := infra.ReadJSON(path, &v)
		assert.Error(t, err)
	})
}

// --- EnsureDir ---
// Rationale: Simple MkdirAll wrapper used for cache and config directory setup.

func TestEnsureDir(t *testing.T) {
	t.Run("creates_directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "new_dir")
		err := infra.EnsureDir(dir, 0755)
		require.NoError(t, err)

		fi, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
	})

	t.Run("existing_directory_is_noop", func(t *testing.T) {
		dir := t.TempDir()
		err := infra.EnsureDir(dir, 0755)
		require.NoError(t, err)
	})

	t.Run("creates_parents", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b", "c")
		err := infra.EnsureDir(dir, 0755)
		require.NoError(t, err)

		fi, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
	})
}

// --- DirSize ---
// Rationale: DirSize calculates directory sizes recursively. Used for cache
// pruning and resource usage reporting.

func TestDirSize(t *testing.T) {
	t.Run("empty_dir_is_zero", func(t *testing.T) {
		dir := t.TempDir()
		got := infra.DirSize(dir)
		assert.Equal(t, int64(0), got)
	})

	t.Run("single_file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0644))
		got := infra.DirSize(dir)
		assert.Equal(t, int64(5), got)
	})

	t.Run("multiple_files_summed", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("12"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("345"), 0644))
		got := infra.DirSize(dir)
		assert.Equal(t, int64(5), got)
	})

	t.Run("subdirectories_included", func(t *testing.T) {
		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		require.NoError(t, os.Mkdir(sub, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "c.txt"), []byte("hello"), 0644))
		got := infra.DirSize(dir)
		assert.Equal(t, int64(5), got)
	})

	t.Run("nonexistent_path_returns_zero", func(t *testing.T) {
		got := infra.DirSize(filepath.Join(t.TempDir(), "nonexistent"))
		assert.Equal(t, int64(0), got)
	})
}

// --- WritePIDFile ---
// Rationale: PID files with flock locking prevent concurrent process conflicts.

func TestWritePIDFile(t *testing.T) {
	t.Run("writes_pid_to_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.pid")

		err := infra.WritePIDFile(path, 12345)
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "12345", string(data))
	})

	t.Run("overwrites_previous_pid", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.pid")

		require.NoError(t, infra.WritePIDFile(path, 111))
		require.NoError(t, infra.WritePIDFile(path, 222))

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "222", string(data))
	})

	t.Run("parent_dir_must_exist", func(t *testing.T) {
		dir := t.TempDir()
		subdir := filepath.Join(dir, "subdir")
		path := filepath.Join(subdir, "test.pid")
		require.NoError(t, infra.EnsureDir(subdir, 0755))

		err := infra.WritePIDFile(path, 42)
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "42", string(data))
	})
}

// --- WaitForSocket ---
// Rationale: Polls for Unix socket creation with timeout. Used for Firecracker
// API socket readiness checks.

func TestWaitForSocket(t *testing.T) {
	t.Run("timeout_when_socket_never_appears", func(t *testing.T) {
		err := infra.WaitForSocket(filepath.Join(t.TempDir(), "nonexistent.sock"), 10*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "did not appear")
	})
}

// --- CopyFile ---
// Rationale: Low-level file copy used throughout the codebase.

func TestCopyFile(t *testing.T) {
	t.Run("copies_file_content", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		dst := filepath.Join(dir, "dst.txt")
		require.NoError(t, os.WriteFile(src, []byte("content"), 0644))

		err := infra.CopyFile(src, dst)
		require.NoError(t, err)

		data, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, "content", string(data))
	})

	t.Run("source_not_found_errors", func(t *testing.T) {
		err := infra.CopyFile(filepath.Join(t.TempDir(), "nonexistent"), filepath.Join(t.TempDir(), "dst"))
		assert.Error(t, err)
	})

	t.Run("binary_copy_preserves_bytes", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.bin")
		dst := filepath.Join(dir, "dst.bin")
		binary := []byte{0x00, 0x01, 0xFF, 0xFE}
		require.NoError(t, os.WriteFile(src, binary, 0644))

		require.NoError(t, infra.CopyFile(src, dst))
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		if diff := cmp.Diff(binary, got); diff != "" {
			t.Errorf("binary copy mismatch (-want +got):\n%s", diff)
		}
	})
}

// --- CopyPreservingMetadata ---
// Rationale: shutil.copy2() equivalent — preserves timestamps and permissions.

func TestCopyPreservingMetadata(t *testing.T) {
	t.Run("copies_and_preserves_permissions", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.sh")
		dst := filepath.Join(dir, "dst.sh")
		require.NoError(t, os.WriteFile(src, []byte("#!/bin/sh\necho hi\n"), 0755))

		err := infra.CopyPreservingMetadata(src, dst)
		require.NoError(t, err)

		dstData, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, "#!/bin/sh\necho hi\n", string(dstData))

		srcInfo, _ := os.Stat(src)
		dstInfo, _ := os.Stat(dst)
		assert.Equal(t, srcInfo.Mode().Perm(), dstInfo.Mode().Perm())
	})

	t.Run("source_not_found_errors", func(t *testing.T) {
		err := infra.CopyPreservingMetadata(
			filepath.Join(t.TempDir(), "nonexistent"),
			filepath.Join(t.TempDir(), "dst"),
		)
		assert.Error(t, err)
	})
}

// --- SafeMove ---
// Rationale: Atomic move with cross-filesystem fallback.

func TestSafeMove(t *testing.T) {
	t.Run("moves_file_to_new_location", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		dst := filepath.Join(dir, "dst.txt")
		require.NoError(t, os.WriteFile(src, []byte("movable"), 0644))

		err := infra.SafeMove(src, dst)
		require.NoError(t, err)

		_, err = os.Stat(src)
		assert.True(t, os.IsNotExist(err))

		data, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, "movable", string(data))
	})

	t.Run("source_not_found_errors", func(t *testing.T) {
		err := infra.SafeMove(filepath.Join(t.TempDir(), "nonexistent"), filepath.Join(t.TempDir(), "dst"))
		assert.Error(t, err)
	})
}

// --- IsSubDir ---
// Rationale: Path containment check used for security boundary enforcement.
// Must reject paths that only match by string prefix (not actual hierarchy).

func TestIsSubDir(t *testing.T) {
	tests := map[string]struct {
		path   string
		parent string
		want   bool
	}{
		"direct_child":       {path: "/home/user/sub", parent: "/home/user", want: true},
		"grandchild":         {path: "/home/user/a/b", parent: "/home/user", want: true},
		"exact_match":        {path: "/home/user", parent: "/home/user", want: true},
		"unrelated_path":     {path: "/other/path", parent: "/home/user", want: false},
		"prefix_trap":        {path: "/home/user-extra", parent: "/home/user", want: false},
		"prefix_trap_nested": {path: "/home/user-extra/sub", parent: "/home/user", want: false},
		"empty_path":         {path: "", parent: "/home", want: false},
		"parent_traversal":   {path: "/home/user/../../etc", parent: "/home/user", want: false},
		"current_dir":        {path: ".", parent: ".", want: true},
		"relative_child":     {path: "a/b/c", parent: "a", want: true},
		"relative_not_child": {path: "../other", parent: "a", want: false},
		"sibling_via_dotdot": {path: "a/../b", parent: "a", want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.IsSubDir(tc.path, tc.parent)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("IsSubDir(%q, %q) mismatch (-want +got):\n%s", tc.path, tc.parent, diff)
			}
		})
	}
}

// --- ReadInt ---
// Rationale: Reads int from /proc-style files. Used for host resource detection.

func TestReadInt(t *testing.T) {
	t.Run("reads_first_field", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "procfile")
		require.NoError(t, os.WriteFile(path, []byte("42\n"), 0644))

		got := infra.ReadInt(path, 0)
		assert.Equal(t, 42, got)
	})

	t.Run("reads_first_of_multiple_fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multi")
		require.NoError(t, os.WriteFile(path, []byte("100 200 300\n"), 0644))

		got := infra.ReadInt(path, 0)
		assert.Equal(t, 100, got)
	})

	t.Run("nonexistent_returns_default", func(t *testing.T) {
		got := infra.ReadInt("/nonexistent", 99)
		assert.Equal(t, 99, got)
	})

	t.Run("empty_file_returns_default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty")
		require.NoError(t, os.WriteFile(path, []byte{}, 0644))

		got := infra.ReadInt(path, -1)
		assert.Equal(t, -1, got)
	})

	t.Run("non_numeric_returns_default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "text")
		require.NoError(t, os.WriteFile(path, []byte("not a number\n"), 0644))

		got := infra.ReadInt(path, 0)
		assert.Equal(t, 0, got)
	})

	t.Run("zero_value_from_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "zero")
		require.NoError(t, os.WriteFile(path, []byte("0\n"), 0644))

		got := infra.ReadInt(path, 99)
		assert.Equal(t, 0, got)
	})
}

// --- SecureMkdir ---
// Rationale: Creates directories with symlink-attack resistance. Used for
// cache and config directory creation in privileged contexts.

// --- PersistenceChain ---
// Validates the write → fsync → close → copy → verify chain.
// This simulates the exact data path used by mvm cp (agent writes a
// file with f.Sync()+f.Close()) followed by base image creation (CopyFile).
// Regression test for: CacheType "Unsafe" in Firecracker causing data loss
// when guest fsync does not trigger host fsync on the backing file.
func TestPersistenceChain_WriteSyncCopyVerify(t *testing.T) {
	t.Run("data_survives_fsync_then_copy", func(t *testing.T) {
		dir := t.TempDir()

		// 1. Create a rootfs file (simulates a newly created ext4 image file)
		rootfsPath := filepath.Join(dir, "rootfs.ext4")
		rootfsFile, err := os.Create(rootfsPath)
		require.NoError(t, err)

		// Write data to the rootfs (simulates the agent writing within the VM)
		expectedData := []byte("this-data-must-survive-copy-" + time.Now().String())
		_, err = rootfsFile.Write(expectedData)
		require.NoError(t, err)

		// 2. fsync (simulates the agent's f.Sync() call)
		err = rootfsFile.Sync()
		require.NoError(t, err)

		// 3. close (simulates the agent's f.Close() call)
		err = rootfsFile.Close()
		require.NoError(t, err)

		// 4. CopyFile (simulates base image creation via infra.CopyFile)
		copyPath := filepath.Join(dir, "rootfs-copy.ext4")
		err = infra.CopyFile(rootfsPath, copyPath)
		require.NoError(t, err)

		// 5. Verify the data survives in the copy
		copiedData, err := os.ReadFile(copyPath)
		require.NoError(t, err)
		assert.True(t, len(copiedData) >= len(expectedData),
			"copied file (%d bytes) must be at least as large as expected data (%d bytes)",
			len(copiedData), len(expectedData))
		assert.Equal(t, expectedData, copiedData[:len(expectedData)],
			"data must survive write → fsync → close → CopyFile chain")

		// 6. Also verify: CopyFile does fdatasync on the destination.
		// Open the copy and verify it's readable after yet another copy.
		recopyPath := filepath.Join(dir, "rootfs-recopy.ext4")
		err = infra.CopyFile(copyPath, recopyPath)
		require.NoError(t, err)
		recopiedData, err := os.ReadFile(recopyPath)
		require.NoError(t, err)
		assert.Equal(t, expectedData, recopiedData[:len(expectedData)],
			"data must survive two consecutive CopyFile operations")
	})

	t.Run("sendfile_and_iocopy_produce_identical_output", func(t *testing.T) {
		dir := t.TempDir()
		original := filepath.Join(dir, "original.img")
		require.NoError(t, os.WriteFile(original, []byte("test-data-for-copy-verification\n"), 0644))

		// Sendfile copy path
		sendfileDst := filepath.Join(dir, "sendfile-copy.img")
		err := infra.CopyFile(original, sendfileDst)
		require.NoError(t, err)

		// io.Copy copy path
		ioDst := filepath.Join(dir, "iocopy-copy.img")
		err = infra.CopyFile(original, ioDst)
		require.NoError(t, err)

		sendfileData, _ := os.ReadFile(sendfileDst)
		ioData, _ := os.ReadFile(ioDst)
		originalData, _ := os.ReadFile(original)
		assert.True(t, len(sendfileData) >= len(originalData), "sendfile copy too small")
		assert.True(t, len(ioData) >= len(originalData), "io.Copy copy too small")
		assert.Equal(t, originalData, sendfileData[:len(originalData)], "sendfile copy must match")
		assert.Equal(t, originalData, ioData[:len(originalData)], "io.Copy copy must match")
	})

	t.Run("data_survives_after_close_and_reopen", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.img")

		// Write and close
		f, err := os.Create(path)
		require.NoError(t, err)
		_, err = f.Write([]byte("persistent-data-123"))
		require.NoError(t, err)
		require.NoError(t, f.Sync())
		require.NoError(t, f.Close())

		// Reopen and copy (simulates: VM stop → reopen rootfs → CopyFile)
		copyDst := filepath.Join(dir, "test-copy.img")
		require.NoError(t, infra.CopyFile(path, copyDst))

		data, err := os.ReadFile(copyDst)
		require.NoError(t, err)
		assert.Contains(t, string(data), "persistent-data-123")
	})

	t.Run("multiple_writes_with_sync_preserve_all", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multi.img")
		f, err := os.Create(path)
		require.NoError(t, err)

		// Simulate multiple file writes (like copying multiple files via mvm cp)
		for i := range 10 {
			chunk := []byte(fmt.Sprintf("chunk-%d-data-", i))
			_, err = f.Write(chunk)
			require.NoError(t, err)
		}
		require.NoError(t, f.Sync())
		require.NoError(t, f.Close())

		copyDst := filepath.Join(dir, "multi-copy.img")
		require.NoError(t, infra.CopyFile(path, copyDst))

		data, err := os.ReadFile(copyDst)
		require.NoError(t, err)
		for i := range 10 {
			assert.Contains(t, string(data), fmt.Sprintf("chunk-%d-data-", i))
		}
	})

	t.Run("zero_length_file_survives_copy", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.img")
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, f.Sync())
		require.NoError(t, f.Close())

		copyDst := filepath.Join(dir, "empty-copy.img")
		require.NoError(t, infra.CopyFile(path, copyDst))

		fi, err := os.Stat(copyDst)
		require.NoError(t, err)
		assert.Equal(t, int64(0), fi.Size(), "zero-length file must remain zero-length")
	})
}

func TestSecureMkdir(t *testing.T) {
	t.Run("creates_new_directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "newdir")
		err := infra.SecureMkdir(dir, "test")
		require.NoError(t, err)

		fi, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
	})

	t.Run("existing_directory_errors", func(t *testing.T) {
		dir := t.TempDir()
		err := infra.SecureMkdir(dir, "test")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("path_with_file_instead_of_dir_errors", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "afile")
		require.NoError(t, os.WriteFile(filePath, []byte("data"), 0644))

		// Trying to create a dir where a file exists should fail
		err := infra.SecureMkdir(filepath.Join(filePath, "sub"), "test")
		assert.Error(t, err)
	})

	t.Run("symlink_path_is_rejected", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		link := filepath.Join(dir, "mylink")
		require.NoError(t, os.Mkdir(target, 0755))
		require.NoError(t, os.Symlink(target, link))

		err := infra.SecureMkdir(link, "test")
		assert.Error(t, err, "SecureMkdir must reject symlink paths")
	})
}
