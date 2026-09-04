//go:build integration

// Integration tests against a real SSH host. Run with:
//
//	go test -tags integration ./internal/manager/ -run "Sftp|Transfer" -v
//
// They load connection details from the repo-root config.json and expect a
// reachable "ubuntu" host. These tests are excluded from the default build.
package manager

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

// newSftpTestManager loads the repo config, relaxes the local path scope so
// t.TempDir files are allowed, and returns a manager.
func newSftpTestManager(t *testing.T) *Manager {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(wd, "..", "..", "config.json")
	opts, err := config.ParseArgs([]string{"--config-file", cfgPath})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ubuntu, ok := opts.Configs["ubuntu"]
	if !ok {
		t.Fatal("config.json must define an 'ubuntu' connection for integration tests")
	}
	ubuntu.LocalPathMode = config.LocalPathModeAny
	m, err := New(opts.Configs, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// sftpTestDir creates a fresh remote directory for one test.
func sftpTestDir(t *testing.T, m *Manager) string {
	t.Helper()
	dir := fmt.Sprintf("/tmp/2native-ssh-mcp-sftp-test-%s", strings.ReplaceAll(strings.ToLower(randomID("d")), "_", ""))
	res, err := m.ExecuteCommand(nil, "mkdir -p "+dir, "", "ubuntu", RunOptions{Prevalidated: true})
	if err != nil {
		t.Fatalf("mkdir remote test dir: %v (%s)", err, res.Stdout)
	}
	t.Cleanup(func() {
		_, _ = m.ExecuteCommand(nil, "rm -rf "+dir, "", "ubuntu", RunOptions{Prevalidated: true})
	})
	return dir
}

func writeFile(t *testing.T, dir string, size int, seed byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("src-%d-%d.bin", size, seed))
	data := make([]byte, size)
	for i := range data {
		data[i] = seed + byte(i%251)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func remoteMD5(t *testing.T, m *Manager, remotePath string) string {
	t.Helper()
	res, err := m.ExecuteCommand(nil, "md5sum "+remotePath+" | awk '{print $1}'", "", "ubuntu", RunOptions{Prevalidated: true})
	if err != nil {
		t.Fatalf("md5sum %s: %v", remotePath, err)
	}
	return strings.TrimSpace(res.Stdout)
}

func localMD5(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", md5.Sum(data))
}

// TestTransferRoundTripAtomic covers the core issue #2 Slice A guarantee:
// the upload lands via a same-directory .part + rename, the target is
// byte-identical, and no .part is left behind; the download matches too.
func TestTransferRoundTripAtomic(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	local := writeFile(t, t.TempDir(), 2<<20, 7) // 2 MiB: exercises the concurrent path
	remote := dir + "/file.bin"

	res, err := m.TransferFile(nil, "upload", local, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.Skipped || res.Resumed {
		t.Fatalf("fresh upload must not be skipped/resumed: %+v", res)
	}
	if got := remoteMD5(t, m, remote); got != localMD5(t, local) {
		t.Fatalf("remote content mismatch after upload")
	}
	// The .part must be gone after a successful upload.
	res2, err := m.ExecuteCommand(nil, "ls "+remote+".part 2>/dev/null | wc -l", "", "ubuntu", RunOptions{Prevalidated: true})
	if err != nil || strings.TrimSpace(res2.Stdout) != "0" {
		t.Fatalf(".part must not survive a successful upload (stdout=%s err=%v)", res2.Stdout, err)
	}

	down := filepath.Join(t.TempDir(), "down.bin")
	if _, err := m.TransferFile(nil, "download", down, remote, "ubuntu", false, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if localMD5(t, down) != localMD5(t, local) {
		t.Fatalf("downloaded content mismatch")
	}

	// Pooled SFTP clients must be present for both pool classes.
	m.mu.Lock()
	poolKeys := len(m.sftpPool)
	m.mu.Unlock()
	if poolKeys == 0 {
		t.Fatal("expected pooled SFTP clients after transfers")
	}
}

func TestTransferSkipWhenMatch(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	local := writeFile(t, t.TempDir(), 64<<10, 3)
	remote := dir + "/file.bin"

	if _, err := m.TransferFile(nil, "upload", local, remote, "ubuntu", false, nil); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	res, err := m.TransferFile(nil, "upload", local, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("second upload must be skipped (same size+mtime), got: %+v", res)
	}
}

func TestTransferUploadResumesFromPart(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	local := writeFile(t, t.TempDir(), 1<<20, 11)
	remote := dir + "/file.bin"
	part := remote + ".part"

	// Simulate an interrupted transfer: seed the .part with a matching
	// prefix by uploading a truncated copy to the .part path.
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	prefixPath := filepath.Join(t.TempDir(), "prefix.bin")
	prefix := data[:512*1024]
	if err := os.WriteFile(prefixPath, prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.TransferFile(nil, "upload", prefixPath, part, "ubuntu", false, nil); err != nil {
		t.Fatalf("seed .part: %v", err)
	}

	upload, err := m.TransferFile(nil, "upload", local, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("resumed upload: %v", err)
	}
	if !upload.Resumed || upload.ResumedFrom != int64(len(prefix)) {
		t.Fatalf("upload must resume from %d bytes, got: %+v", len(prefix), upload)
	}
	if got := remoteMD5(t, m, remote); got != localMD5(t, local) {
		t.Fatalf("remote content mismatch after resumed upload")
	}
}

func TestTransferUploadRejectsDirectoryTarget(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	local := writeFile(t, t.TempDir(), 1024, 5)

	_, err := m.TransferFile(nil, "upload", local, dir, "ubuntu", false, nil)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("upload to a directory must be rejected early, got: %v", err)
	}
}

func TestTransferDownloadRejectsDirectorySource(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	down := filepath.Join(t.TempDir(), "out.bin")

	_, err := m.TransferFile(nil, "download", down, dir, "ubuntu", false, nil)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("download of a directory must be rejected early, got: %v", err)
	}
}

// TestTransferDirectoryRecursive covers issue #2 directory transfers: a local
// tree uploads recursively (remote parents auto-created), then downloads back
// byte-identical. Directory transfers aggregate per-file results and skip the
// single-file sha256 pass.
func TestTransferDirectoryRecursive(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)

	localRoot := filepath.Join(t.TempDir(), "tree")
	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/deep/c.bin"} {
		p := filepath.Join(localRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	remote := dir + "/tree"

	up, err := m.TransferFile(nil, "upload", localRoot, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("directory upload: %v", err)
	}
	if up.Files != 3 || len(up.Failed) != 0 {
		t.Fatalf("expected 3 files uploaded, got: %+v", up)
	}
	res, err := m.ExecuteCommand(nil, "find "+remote+" -type f | wc -l", "", "ubuntu", RunOptions{Prevalidated: true})
	if err != nil || strings.TrimSpace(res.Stdout) != "3" {
		t.Fatalf("remote tree must contain 3 files (stdout=%s err=%v)", res.Stdout, err)
	}
	if up.ChecksumStatus != "" {
		t.Fatalf("directory transfer must not claim a single-file checksum: %+v", up)
	}

	down := filepath.Join(t.TempDir(), "out")
	dres, err := m.TransferFile(nil, "download", down, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("directory download: %v", err)
	}
	if dres.Files != 3 {
		t.Fatalf("expected 3 files downloaded, got: %+v", dres)
	}
	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/deep/c.bin"} {
		data, err := os.ReadFile(filepath.Join(down, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("downloaded %s: %v", rel, err)
		}
		if string(data) != "content of "+rel {
			t.Fatalf("content mismatch for %s: %q", rel, data)
		}
	}
}

func TestTransferChecksumVerified(t *testing.T) {
	m := newSftpTestManager(t)
	dir := sftpTestDir(t, m)
	local := writeFile(t, t.TempDir(), 128*1024, 42)
	remote := dir + "/file.bin"

	res, err := m.TransferFile(nil, "upload", local, remote, "ubuntu", false, nil)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.ChecksumStatus != "verified" {
		t.Fatalf("checksum must be verified against ubuntu (sha256sum), got: %+v", res)
	}
	if res.Checksum != localMD5(t, local) && res.Checksum == "" {
		t.Fatalf("checksum must be recorded: %+v", res)
	}
}
