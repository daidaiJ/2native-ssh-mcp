package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

const resumeSampleBytes = 32 * 1024

// uploadPartSuffix marks the in-progress remote side of an upload. It always
// lives in the same directory as the target so the final move is a same-filesystem
// rename, and the target itself is never truncated by a failed transfer.
const uploadPartSuffix = ".part"

// sftpPoolIdleTTL bounds how long an unused pooled SFTP client stays open.
const sftpPoolIdleTTL = 5 * time.Minute

// maxSftpPacket is the practical SFTP packet ceiling (OpenSSH accepts up to
// 256 KiB); larger configured chunks stop paying off beyond it.
const maxSftpPacket = 256 * 1024

// TransferResult summarizes a completed file transfer.
type TransferResult struct {
	Action     string        `json:"action"`
	LocalPath  string        `json:"localPath"`
	RemotePath string        `json:"remotePath"`
	Bytes      int64         `json:"bytes"`
	Elapsed    time.Duration `json:"-"`
	SpeedBps   float64       `json:"speedBps"`
	Percent    float64       `json:"percent"`
	// Skipped is true when the destination already matches the source
	// (same size and mtime) and the transfer was skipped.
	Skipped bool `json:"skipped,omitempty"`
	// Resumed is true when the transfer continued from an existing partial
	// destination; ResumedFrom is the byte offset it started at.
	Resumed     bool  `json:"resumed,omitempty"`
	ResumedFrom int64 `json:"resumedFrom,omitempty"`
	// Checksum is the sha256 agreed by both sides after a single-file
	// transfer; ChecksumStatus is "verified" or "unverified" (the remote has
	// no sha256sum). Directory transfers leave both empty.
	Checksum       string `json:"checksum,omitempty"`
	ChecksumStatus string `json:"checksumStatus,omitempty"`
	// ElapsedS is the wall time in seconds (rounded to 3 decimals) — a
	// universal unit that agents read without conversion. The raw duration
	// stays available in-process via Elapsed.
	ElapsedS float64 `json:"elapsedS"`
	// Directory aggregates: how many per-file entries were deduplicated or
	// resumed. Zero (omitted) means every file was freshly transferred.
	SkippedFiles int `json:"skippedFiles,omitempty"`
	ResumedFiles int `json:"resumedFiles,omitempty"`
	// Files is the transferred file count in directory mode; Failed lists
	// per-file errors as "<rel path>: <error>".
	Files  int      `json:"files,omitempty"`
	Failed []string `json:"failed,omitempty"`
}

// ProgressFunc reports transfer progress. total is -1 when the size is
// unknown.
type ProgressFunc func(done, total int64)

// TransferFile uploads or downloads a file. Uploads are atomic: data goes to
// <remote>.part in the target directory and is renamed into place only after
// the byte count is verified, so a failed transfer never truncates the
// target. Transfers are deduplicated (skipped when the destination already
// matches) and resumed from an existing .part unless force is set.
// sftpTimeoutMs bounds time *without progress*, not the total duration, so a
// large but healthy transfer is not cut off.
func (m *Manager) TransferFile(ctx context.Context, action, localPath, remotePath, name string, force bool, progress ProgressFunc) (*TransferResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := m.getConfig(name)
	if err != nil {
		return nil, err
	}
	key := m.resolveName(name)
	m.beginOp(key)
	defer m.endOp(key)
	if cfg.TransportMode == "shell" {
		return nil, newToolError(CodeUnsupportedInShellMode,
			"Current bastion shell mode does not support SFTP upload/download.", false)
	}

	var validatedLocal, validatedRemote string
	if action == "upload" {
		validatedLocal, err = m.validateLocalPath(localPath, name, "read")
	} else {
		validatedLocal, err = m.validateLocalPath(localPath, name, "write")
	}
	if err != nil {
		return nil, err
	}
	validatedRemote, err = m.validateRemotePath(remotePath, name)
	if err != nil {
		return nil, err
	}

	client, err := m.EnsureConnected(name)
	if err != nil {
		return nil, err
	}

	// Idle-on-progress watchdog: the timeout resets on every progress tick,
	// so it aborts stalled transfers instead of capping total duration.
	sftpTimeout := time.Duration(cfg.SftpTimeoutMs) * time.Millisecond
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchdog := time.AfterFunc(sftpTimeout, cancel)
	defer watchdog.Stop()
	progress = idleProgress(progress, watchdog, sftpTimeout)

	sftpClient, err := m.acquireSftp(tctx, client, key, cfg, false)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if tctx.Err() != nil || !succeeded {
			_ = sftpClient.Close()
			return
		}
		m.releaseSftp(key, false, sftpClient)
	}()
	stop := context.AfterFunc(tctx, func() { _ = sftpClient.Close() })
	defer stop()

	var result *TransferResult
	switch {
	case action == "upload" && isLocalDir(validatedLocal):
		result, err = m.uploadDirectory(tctx, sftpClient, client, key, validatedLocal, validatedRemote, cfg, force, progress)
	case action == "download":
		if fi, statErr := sftpClient.Stat(validatedRemote); statErr == nil && fi.IsDir() {
			result, err = m.downloadDirectory(tctx, sftpClient, validatedRemote, validatedLocal, cfg, force, progress)
		} else {
			result, err = m.download(tctx, sftpClient, validatedRemote, validatedLocal, cfg, force, progress)
		}
	default:
		result, err = m.upload(tctx, sftpClient, client, key, validatedLocal, validatedRemote, cfg, force, progress)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctxToolError(ctx, CodeOperationTimeout, "File transfer cancelled")
		}
		if tctx.Err() != nil {
			return nil, ctxToolError(tctx, CodeOperationTimeout,
				fmt.Sprintf("File transfer aborted: no progress for %dms (idle timeout)", sftpTimeout.Milliseconds()))
		}
		return nil, err
	}
	succeeded = true

	// Content integrity for single-file transfers: sha256 both sides after
	// the copy. A remote without sha256sum marks the result "unverified"
	// rather than failing it; a mismatch is a hard error.
	if result.Files == 0 && !result.Skipped {
		sum, status, cerr := m.verifyTransferChecksum(tctx, client, action, validatedLocal, validatedRemote)
		if cerr != nil {
			return nil, cerr
		}
		result.Checksum, result.ChecksumStatus = sum, status
	}

	m.Touch(key, DefaultKeepAliveDuration)
	return result, nil
}

func isLocalDir(path string) bool {
	if fi, err := os.Stat(path); err == nil {
		return fi.IsDir()
	}
	return false
}

// sha256File returns the hex sha256 of a local file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyTransferChecksum hashes the local file and runs sha256sum on the
// remote over a fresh exec channel. A remote without the tool yields
// "unverified"; a mismatch is a hard, retriable error.
func (m *Manager) verifyTransferChecksum(ctx context.Context, client *ssh.Client, action, localPath, remotePath string) (string, string, error) {
	localSum, err := sha256File(localPath)
	if err != nil {
		return "", "", newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Checksum verification failed: cannot read %s: %v", localPath, err), false)
	}
	script := fmt.Sprintf("sha256sum -- %s 2>/dev/null | awk '{print $1}'", shellQuote(remotePath))
	out, err := m.runDetachedExec(ctx, client, script, 60*time.Second)
	remoteSum := strings.TrimSpace(out)
	if err != nil || len(remoteSum) != 64 {
		return "", "unverified", nil
	}
	if remoteSum != localSum {
		return "", "", newToolError(CodeSFTPError,
			fmt.Sprintf("Checksum mismatch after %s: local sha256=%s remote sha256=%s — the transfer did not land intact; re-run with force",
				action, localSum, remoteSum), true)
	}
	return localSum, "verified", nil
}

// transferTask is one file inside a directory transfer, with its path
// relative to the transfer root (slash-separated, for both sides).
type transferTask struct {
	local  string
	remote string
	rel    string
	size   int64
}

// uploadDirectory transfers a local directory tree to remoteRoot, reusing the
// single-file upload path per entry (atomic .part + rename, dedup, resume).
// It returns an aggregate result; per-file failures are collected in Failed
// and do not abort the remaining files. Empty local directories are not
// recreated remotely.
func (m *Manager) uploadDirectory(ctx context.Context, sftpClient *sftp.Client, client *ssh.Client, key string,
	localRoot, remoteRoot string, cfg *config.SSHConfig, force bool, progress ProgressFunc) (*TransferResult, error) {

	start := time.Now()
	var tasks []transferTask
	var total int64
	walkErr := filepath.WalkDir(localRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(localRoot, p)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		relSlash := filepath.ToSlash(rel)
		tasks = append(tasks, transferTask{local: p, remote: remoteRoot + "/" + relSlash, rel: relSlash, size: info.Size()})
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Failed to walk local directory %s: %v", localRoot, walkErr), false)
	}
	if len(tasks) == 0 {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Local directory has no files to upload: %s", localRoot), false)
	}

	if err := sftpClient.MkdirAll(remoteRoot); err != nil {
		return nil, classifySftpError("upload", localRoot, remoteRoot, err)
	}

	var done, transferred int64
	var failed []string
	var skippedFiles, resumedFiles int
	for _, t := range tasks {
		if err := sftpClient.MkdirAll(posixDir(t.remote)); err != nil {
			failed = append(failed, fmt.Sprintf("%s: create remote directory: %v", t.rel, err))
			continue
		}
		up, err := m.upload(ctx, sftpClient, client, key, t.local, t.remote, cfg, force, offsetProgress(progress, done, total))
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.rel, err))
			continue
		}
		transferred += up.Bytes
		if up.Skipped {
			skippedFiles++
		}
		if up.Resumed {
			resumedFiles++
		}
		done += t.size
	}

	elapsed := time.Since(start)
	res := &TransferResult{
		Action:       "upload",
		LocalPath:    localRoot,
		RemotePath:   remoteRoot,
		Bytes:        transferred,
		Elapsed:      elapsed,
		SpeedBps:     speed(transferred, elapsed),
		Percent:      percent(done, total),
		Files:        len(tasks) - len(failed),
		SkippedFiles: skippedFiles,
		ResumedFiles: resumedFiles,
	}
	if len(failed) > 0 {
		res.Failed = failed
	}
	return res, nil
}

// downloadDirectory transfers a remote directory tree to a local directory,
// mirroring uploadDirectory: per-file download path (temp + atomic rename,
// dedup), aggregate result, failures collected without aborting.
func (m *Manager) downloadDirectory(ctx context.Context, sftpClient *sftp.Client,
	remoteRoot, localRoot string, cfg *config.SSHConfig, force bool, progress ProgressFunc) (*TransferResult, error) {

	start := time.Now()
	var tasks []transferTask
	var total int64
	walker := sftpClient.Walk(remoteRoot)
	for walker.Step() {
		if walker.Err() != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("Failed to walk remote directory %s: %v", remoteRoot, walker.Err()), true)
		}
		path := walker.Path()
		info := walker.Stat()
		if info == nil || info.IsDir() {
			continue
		}
		rel := strings.TrimPrefix(path, remoteRoot)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		tasks = append(tasks, transferTask{
			local:  filepath.Join(localRoot, filepath.FromSlash(rel)),
			remote: path,
			rel:    rel,
			size:   info.Size(),
		})
		total += info.Size()
	}
	if len(tasks) == 0 {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Remote directory has no files to download: %s", remoteRoot), false)
	}

	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return nil, newToolError(CodeLocalFileWriteFailed,
			fmt.Sprintf("Failed to create local directory %s: %v", localRoot, err), false)
	}

	var done, transferred int64
	var failed []string
	var skippedFiles, resumedFiles int
	for _, t := range tasks {
		if err := os.MkdirAll(filepath.Dir(t.local), 0o755); err != nil {
			failed = append(failed, fmt.Sprintf("%s: create local directory: %v", t.rel, err))
			continue
		}
		down, err := m.download(ctx, sftpClient, t.remote, t.local, cfg, force, offsetProgress(progress, done, total))
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", t.rel, err))
			continue
		}
		transferred += down.Bytes
		if down.Skipped {
			skippedFiles++
		}
		if down.Resumed {
			resumedFiles++
		}
		done += t.size
	}

	elapsed := time.Since(start)
	res := &TransferResult{
		Action:       "download",
		LocalPath:    localRoot,
		RemotePath:   remoteRoot,
		Bytes:        transferred,
		Elapsed:      elapsed,
		SpeedBps:     speed(transferred, elapsed),
		Percent:      percent(done, total),
		Files:        len(tasks) - len(failed),
		SkippedFiles: skippedFiles,
		ResumedFiles: resumedFiles,
	}
	if len(failed) > 0 {
		res.Failed = failed
	}
	return res, nil
}

// ctxDeadlineMs reports how long the context had been given, for error
// messages; 0 when unknown.
func ctxDeadlineMs(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline).Milliseconds()
	}
	return 0
}

var _ = ctxDeadlineMs // retained for future timeout messages

// idleProgress wraps progress so every callback also resets the no-progress
// watchdog.
func idleProgress(progress ProgressFunc, watchdog *time.Timer, idle time.Duration) ProgressFunc {
	return func(done, total int64) {
		watchdog.Reset(idle)
		if progress != nil {
			progress(done, total)
		}
	}
}

// sftpPoolEntry is a cached SFTP client for a connection.
type sftpPoolEntry struct {
	client *sftp.Client
	timer  *time.Timer
}

// acquireSftp returns an SFTP client for the connection, reusing a pooled one
// when available. cw selects the concurrent-writes client (a separate client
// so pipelined, possibly hole-leaving writes never interleave with the
// resume-safe sequential client). The returned release puts the client back
// into the pool; callers must Close it themselves instead when the transfer
// failed.
func (m *Manager) acquireSftp(ctx context.Context, client *ssh.Client, key string, cfg *config.SSHConfig, cw bool) (*sftp.Client, error) {
	poolKey := key
	if cw {
		poolKey += "#cw"
	}
	m.mu.Lock()
	if e, ok := m.sftpPool[poolKey]; ok {
		delete(m.sftpPool, poolKey)
		if e.timer != nil {
			e.timer.Stop()
		}
		m.mu.Unlock()
		return e.client, nil
	}
	m.mu.Unlock()
	return m.openSftp(ctx, client, key, cfg, cw)
}

// releaseSftp returns a healthy client to the connection's pool with an idle
// TTL; a duplicate or expired entry is closed.
func (m *Manager) releaseSftp(key string, cw bool, client *sftp.Client) {
	if client == nil {
		return
	}
	poolKey := key
	if cw {
		poolKey += "#cw"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sftpPool[poolKey]; exists {
		_ = client.Close()
		return
	}
	e := &sftpPoolEntry{client: client}
	e.timer = time.AfterFunc(sftpPoolIdleTTL, func() {
		m.mu.Lock()
		delete(m.sftpPool, poolKey)
		m.mu.Unlock()
		_ = client.Close()
	})
	m.sftpPool[poolKey] = e
}

// openSftp opens the SFTP subsystem, bounded by ctx. Concurrent writes are
// enabled only on the cw client: pkg/sftp may leave holes in a file after a
// failed concurrent write (pkg/sftp#472), which is acceptable for a fresh
// .part that is deleted on failure, but never for a resume-safe partial.
func (m *Manager) openSftp(ctx context.Context, client *ssh.Client, key string, cfg *config.SSHConfig, cw bool) (*sftp.Client, error) {
	type result struct {
		client *sftp.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		var opts []sftp.ClientOption
		if cw {
			opts = append(opts, sftp.UseConcurrentWrites(true))
		}
		if cfg != nil && cfg.SftpChunkSize >= 1024 {
			maxPacket := cfg.SftpChunkSize
			if maxPacket > maxSftpPacket {
				maxPacket = maxSftpPacket
			}
			opts = append(opts, sftp.MaxPacket(maxPacket))
		}
		c, err := sftp.NewClient(client, opts...)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("SFTP connection failed: %v", r.err), true)
		}
		return r.client, nil
	case <-ctx.Done():
		m.Disconnect(key)
		return nil, ctxToolError(ctx, CodeOperationTimeout, "SFTP open timed out")
	}
}

// classifySftpError maps a remote SFTP operation failure to a precise
// message: missing remote parent directory (upload), missing remote file
// (download), permission denied, or a generic fallback that keeps both
// paths. It never auto-creates directories; the message says so. pkg/sftp
// normalises NO_SUCH_FILE/PERMISSION_DENIED to os.ErrNotExist/os.ErrPermission,
// so both the stdlib forms and raw *sftp.StatusError are handled.
func classifySftpError(op, local, remote string, err error) error {
	var statusErr *sftp.StatusError
	noSuchFile := errors.Is(err, os.ErrNotExist)
	permission := errors.Is(err, os.ErrPermission)
	if errors.As(err, &statusErr) {
		switch statusErr.Code {
		case uint32(sftp.ErrSSHFxNoSuchFile):
			noSuchFile = true
		case uint32(sftp.ErrSSHFxPermissionDenied):
			permission = true
		}
	}
	switch {
	case noSuchFile:
		if op == "upload" {
			return newToolError(CodeSFTPError,
				fmt.Sprintf("Remote parent directory does not exist: %s (local file exists: %s). Create the directory first.", posixDir(remote), local), false)
		}
		return newToolError(CodeSFTPError,
			fmt.Sprintf("Remote file does not exist: %s", remote), false)
	case permission:
		return newToolError(CodeSFTPError,
			fmt.Sprintf("Remote permission denied: %s", remote), false)
	}
	return newToolError(CodeSFTPError,
		fmt.Sprintf("File %s failed: %v (local=%s remote=%s)", op, err, local, remote), true)
}

func (m *Manager) upload(ctx context.Context, sftpClient *sftp.Client, client *ssh.Client, key string,
	localPath, remotePath string, cfg *config.SSHConfig, force bool, progress ProgressFunc) (*TransferResult, error) {

	localFile, err := os.Open(localPath)
	if err != nil {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Failed to read local file: %v", err), false)
	}
	defer localFile.Close()

	localStat, err := localFile.Stat()
	if err != nil {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Failed to read local file: %v", err), false)
	}
	if localStat.IsDir() {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Local path is a directory, not a file: %s", localPath), false)
	}
	total := localStat.Size()
	localMtime := localStat.ModTime()
	partPath := remotePath + uploadPartSuffix

	start := time.Now()
	var done int64
	resumed := false
	var resumedFrom int64

	remoteStat, statErr := sftpClient.Stat(remotePath)
	if statErr == nil && remoteStat.IsDir() {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Remote path is a directory, not a file: %s", remotePath), false)
	}
	if statErr == nil && !force {
		remoteSize := remoteStat.Size()
		if remoteSize == total && remoteStat.ModTime().Unix() == localMtime.Unix() {
			_ = sftpClient.Remove(partPath) // best-effort: drop a stale .part
			return &TransferResult{
				Action:     "upload",
				LocalPath:  localPath,
				RemotePath: remotePath,
				Bytes:      0,
				Elapsed:    time.Since(start),
				Percent:    100,
				Skipped:    true,
			}, nil
		}
	}

	// Concurrent copy is used for large transfers; it never taints the
	// resume-safe prefix: writes only happen at/after the resume offset, so
	// a failed concurrent resume is truncated back to the confirmed prefix
	// (a failed concurrent FRESH write can leave holes anywhere, so that
	// .part is removed outright — pkg/sftp#472).
	chunkSize := cfg.SftpChunkSize
	concurrent := cfg.SftpConcurrency > 1 && total >= int64(cfg.SftpConcurrency)*int64(chunkSize)

	// Resume from an existing .part, never from the final target.
	if !force && total > 0 {
		if partStat, err := sftpClient.Stat(partPath); err == nil {
			partSize := partStat.Size()
			if partSize > 0 && partSize < total {
				if partRO, err := sftpClient.Open(partPath); err == nil {
					match := overlapMatches(localFile, partRO, partSize)
					partRO.Close()
					if match {
						copyClient := sftpClient
						if concurrent {
							cwClient, cwErr := m.acquireSftp(ctx, client, key, cfg, true)
							if cwErr != nil {
								return nil, cwErr
							}
							copyClient = cwClient
							defer func() {
								if ctx.Err() == nil {
									m.releaseSftp(key, true, cwClient)
								} else {
									_ = cwClient.Close()
								}
							}()
						}
						partFile, err := copyClient.OpenFile(partPath, os.O_WRONLY)
						if err == nil {
							done, err = copyFile(ctx, localFile, partFile, partSize, total,
								cfg.SftpConcurrency, chunkSize, offsetProgress(progress, partSize, total))
							partFile.Close()
							if err != nil {
								if concurrent {
									// Holes can only exist past the confirmed
									// prefix: cut back to it and keep the .part.
									if terr := copyClient.Truncate(partPath, partSize); terr != nil {
										_ = copyClient.Remove(partPath)
									}
								}
								return nil, newToolError(CodeSFTPError,
									fmt.Sprintf("File upload failed (partial kept at %s): %v", partPath, err), true)
							}
							resumed = true
							resumedFrom = partSize
						}
					}
				}
			}
		}
	}

	if !resumed {
		var partFile *sftp.File
		partFile, err = sftpClient.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return nil, classifySftpError("upload", localPath, partPath, err)
		}
		copyWorkers := cfg.SftpConcurrency
		copyClient := sftpClient
		if concurrent {
			// A dedicated concurrent-writes client; released back to its own pool.
			cwClient, cwErr := m.acquireSftp(ctx, client, key, cfg, true)
			if cwErr != nil {
				partFile.Close()
				return nil, cwErr
			}
			copyClient = cwClient
			defer func() {
				if ctx.Err() == nil {
					m.releaseSftp(key, true, cwClient)
				} else {
					_ = cwClient.Close()
				}
			}()
		}
		done, err = copyFile(ctx, localFile, partFile, 0, total, copyWorkers, chunkSize, progress)
		partFile.Close()
		if err != nil {
			if concurrent {
				// Concurrent writes may leave holes: the partial is not
				// trustworthy, so drop it instead of resuming into it later.
				_ = copyClient.Remove(partPath)
			}
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("File upload failed (partial at %s): %v", partPath, err), true)
		}
	}

	// The local file may have grown during the transfer; append the tail.
	tail, err := m.appendUploadTail(ctx, sftpClient, localFile, partPath, total)
	if err != nil {
		return nil, err
	}
	done += tail

	// Byte-count assertion: a short read must never pass as success and must
	// never be renamed into place.
	localSize := total
	if st, statErr := localFile.Stat(); statErr == nil {
		localSize = st.Size()
	}
	partStat, statErr := sftpClient.Stat(partPath)
	if statErr != nil {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File upload failed: cannot verify the partial at %s: %v", partPath, statErr), true)
	}
	if partStat.Size() != localSize {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Upload size mismatch: local=%d uploaded=%d (short transfer); the partial is kept at %s for resume",
				localSize, partStat.Size(), partPath), true)
	}

	if st, statErr := localFile.Stat(); statErr == nil {
		_ = sftpClient.Chtimes(partPath, st.ModTime(), st.ModTime())
	}

	// Atomic move into place. posix-rename@openssh.com replaces the target
	// without a clobber window; the plain rename fallback is used only when
	// the target does not exist (plain rename never overwrites).
	if err := sftpClient.PosixRename(partPath, remotePath); err != nil {
		if _, statErr := sftpClient.Stat(remotePath); statErr == nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("Uploaded data is in %s but the server does not support posix-rename over the existing target %s; move it into place manually",
					partPath, remotePath), false)
		}
		if renameErr := sftpClient.Rename(partPath, remotePath); renameErr != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("Upload succeeded but could not move %s into place: %v", partPath, renameErr), true)
		}
	}

	elapsed := time.Since(start)
	return &TransferResult{
		Action:      "upload",
		LocalPath:   localPath,
		RemotePath:  remotePath,
		Bytes:       done,
		Elapsed:     elapsed,
		SpeedBps:    speed(done, elapsed),
		Percent:     percent(done+resumedFrom, total),
		Resumed:     resumed,
		ResumedFrom: resumedFrom,
	}, nil
}

func (m *Manager) download(ctx context.Context, sftpClient *sftp.Client, remotePath, localPath string,
	cfg *config.SSHConfig, force bool, progress ProgressFunc) (*TransferResult, error) {

	remoteStat, err := sftpClient.Stat(remotePath)
	if err != nil {
		return nil, classifySftpError("download", localPath, remotePath, err)
	}
	if remoteStat.IsDir() {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Remote path is a directory, not a file: %s", remotePath), false)
	}
	total := remoteStat.Size()
	remoteMtime := remoteStat.ModTime()

	if ls, statErr := os.Stat(localPath); statErr == nil && ls.IsDir() {
		return nil, newToolError(CodeLocalFileWriteFailed,
			fmt.Sprintf("Local path is a directory, not a file: %s", localPath), false)
	}

	start := time.Now()

	if !force {
		if localStat, err := os.Stat(localPath); err == nil &&
			localStat.Size() == total && localStat.ModTime().Unix() == remoteMtime.Unix() {
			return &TransferResult{
				Action:     "download",
				LocalPath:  localPath,
				RemotePath: remotePath,
				Bytes:      0,
				Elapsed:    time.Since(start),
				Percent:    100,
				Skipped:    true,
			}, nil
		}
	}

	if !force {
		if localStat, err := os.Stat(localPath); err == nil && localStat.Size() > 0 && localStat.Size() < total {
			if localRO, err := os.Open(localPath); err == nil {
				remoteRO, rerr := sftpClient.Open(remotePath)
				match := false
				if rerr == nil {
					match = overlapMatches(remoteRO, localRO, localStat.Size())
					remoteRO.Close()
				}
				localRO.Close()
				if match {
					localFile, err := os.OpenFile(localPath, os.O_WRONLY, 0)
					if err == nil {
						remoteFile, err := sftpClient.Open(remotePath)
						if err == nil {
							done, copyErr := copyFile(ctx, remoteFile, localFile, localStat.Size(), total,
								cfg.SftpConcurrency, cfg.SftpChunkSize, offsetProgress(progress, localStat.Size(), total))
							remoteFile.Close()
							localFile.Close()
							if copyErr != nil {
								return nil, newToolError(CodeSFTPError,
									fmt.Sprintf("File download failed: %v", copyErr), true)
							}
							_ = os.Chtimes(localPath, time.Now(), remoteMtime)
							elapsed := time.Since(start)
							return &TransferResult{
								Action:      "download",
								LocalPath:   localPath,
								RemotePath:  remotePath,
								Bytes:       done,
								Elapsed:     elapsed,
								SpeedBps:    speed(done, elapsed),
								Percent:     percent(done+localStat.Size(), total),
								Resumed:     true,
								ResumedFrom: localStat.Size(),
							}, nil
						}
						localFile.Close()
					}
				}
			}
		}
	}

	tempPath := fmt.Sprintf("%s.tmp-%d-%d", localPath, os.Getpid(), time.Now().UnixNano())
	tempFile, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, newToolError(CodeLocalFileWriteFailed,
			fmt.Sprintf("Failed to save file: %v", err), false)
	}

	cleanup := func() {
		tempFile.Close()
		os.Remove(tempPath)
	}

	remoteFile, err := sftpClient.Open(remotePath)
	if err != nil {
		cleanup()
		return nil, classifySftpError("download", localPath, remotePath, err)
	}
	defer remoteFile.Close()

	done, err := copyFile(ctx, remoteFile, tempFile, 0, total, cfg.SftpConcurrency, cfg.SftpChunkSize, progress)
	if err != nil {
		cleanup()
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
	}

	tail, err := m.appendDownloadTail(ctx, sftpClient, remoteFile, tempFile, done)
	if err != nil {
		cleanup()
		return nil, err
	}
	done += tail

	// Byte-count assertion: the saved file must match the remote size exactly.
	if cur, statErr := sftpClient.Stat(remotePath); statErr == nil && cur.Size() != total {
		cleanup()
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Download size mismatch: remote grew or shrank during transfer (%d → %d bytes); retry", total, cur.Size()), true)
	}
	if fi, statErr := tempFile.Stat(); statErr == nil && fi.Size() != total {
		cleanup()
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("Download size mismatch: expected %d bytes, saved %d (short transfer); retry", total, fi.Size()), true)
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return nil, newToolError(CodeLocalFileWriteFailed,
			fmt.Sprintf("Failed to save file: %v", err), false)
	}
	if err := os.Rename(tempPath, localPath); err != nil {
		os.Remove(tempPath)
		return nil, newToolError(CodeLocalFileWriteFailed,
			fmt.Sprintf("Failed to save file: %v", err), false)
	}
	_ = os.Chtimes(localPath, time.Now(), remoteMtime)

	elapsed := time.Since(start)
	return &TransferResult{
		Action:     "download",
		LocalPath:  localPath,
		RemotePath: remotePath,
		Bytes:      done,
		Elapsed:    elapsed,
		SpeedBps:   speed(done, elapsed),
		Percent:    percent(done, total),
	}, nil
}

func offsetProgress(progress ProgressFunc, offset, total int64) ProgressFunc {
	if progress == nil {
		return nil
	}
	return func(done, _ int64) {
		progress(done+offset, total)
	}
}

func copyFile(ctx context.Context, src io.ReaderAt, dst io.WriterAt, start, total int64, workers, chunkSize int, progress ProgressFunc) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Sequential when there is nothing to parallelize: fewer chunks than
	// workers means the fan-out costs more than it saves.
	if total <= 0 || workers <= 1 || total < int64(workers)*int64(chunkSize) {
		return copySequential(ctx, src, dst, start, total, chunkSize, progress)
	}
	return copyConcurrent(ctx, src, dst, start, total, workers, chunkSize, progress)
}

func copySequential(ctx context.Context, src io.ReaderAt, dst io.WriterAt, start, total int64, chunkSize int, progress ProgressFunc) (int64, error) {
	buf := make([]byte, chunkSize)
	offset := start
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		n, err := src.ReadAt(buf, offset)
		if n > 0 {
			if _, werr := dst.WriteAt(buf[:n], offset); werr != nil {
				return done, werr
			}
			offset += int64(n)
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if err == io.EOF {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
}

func copyConcurrent(ctx context.Context, src io.ReaderAt, dst io.WriterAt, start, total int64, workers, chunkSize int, progress ProgressFunc) (int64, error) {
	var done atomic.Int64
	var firstErr atomic.Value
	var next atomic.Int64
	var wg sync.WaitGroup
	next.Store(start)

	stop := context.AfterFunc(ctx, func() {
		if err := ctx.Err(); err != nil {
			firstErr.CompareAndSwap(nil, err)
		}
	})
	defer stop()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunkSize)
			for {
				if firstErr.Load() != nil {
					return
				}
				offset := next.Add(int64(chunkSize)) - int64(chunkSize)
				if offset >= total {
					return
				}
				want := int64(chunkSize)
				if offset+want > total {
					want = total - offset
				}
				var got int
				for got < int(want) {
					if firstErr.Load() != nil {
						return
					}
					n, err := src.ReadAt(buf[got:want], offset+int64(got))
					if n > 0 {
						got += n
					}
					if err != nil {
						if err == io.EOF {
							break
						}
						firstErr.CompareAndSwap(nil, err)
						return
					}
					if n == 0 {
						firstErr.CompareAndSwap(nil, io.ErrUnexpectedEOF)
						return
					}
				}
				if got > 0 {
					if _, werr := dst.WriteAt(buf[:got], offset); werr != nil {
						firstErr.CompareAndSwap(nil, werr)
						return
					}
					done.Add(int64(got))
					if progress != nil {
						progress(done.Load(), total)
					}
				}
				if got < int(want) {
					return
				}
			}
		}()
	}
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return done.Load(), e.(error)
	}
	return done.Load(), nil
}

func (m *Manager) appendUploadTail(ctx context.Context, sftpClient *sftp.Client, localFile *os.File,
	remotePath string, sizeBefore int64) (int64, error) {

	currentSize, err := localFile.Stat()
	if err != nil || currentSize.Size() <= sizeBefore {
		return 0, nil
	}
	remoteStat, err := sftpClient.Stat(remotePath)
	if err != nil || remoteStat.Size() >= currentSize.Size() {
		return 0, nil
	}

	remoteFile, err := sftpClient.OpenFile(remotePath, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return 0, newToolError(CodeSFTPError, fmt.Sprintf("File upload failed: %v", err), true)
	}
	defer remoteFile.Close()

	n, err := copySequential(ctx, localFile, remoteFile, remoteStat.Size(), -1, 64*1024, nil)
	if err != nil {
		return n, newToolError(CodeSFTPError, fmt.Sprintf("File upload failed: %v", err), true)
	}
	return n, nil
}

func (m *Manager) appendDownloadTail(ctx context.Context, sftpClient *sftp.Client, remoteFile *sftp.File,
	tempFile *os.File, downloadedBytes int64) (int64, error) {

	remoteStat, err := sftpClient.Stat(remoteFile.Name())
	if err != nil || remoteStat.Size() <= downloadedBytes {
		return 0, nil
	}
	n, err := copySequential(ctx, remoteFile, tempFile, downloadedBytes, -1, 64*1024, nil)
	if err != nil {
		return n, newToolError(CodeSFTPError, fmt.Sprintf("File download failed: %v", err), true)
	}
	return n, nil
}

// overlapMatches checks that dst[0:size] looks like a prefix of src by
// comparing the first and last sample of the overlap. A mismatch means the
// destination is a different file and must not be resumed into.
func overlapMatches(src, dst io.ReaderAt, size int64) bool {
	if size <= 0 {
		return true
	}
	n := size
	if n > resumeSampleBytes {
		n = resumeSampleBytes
	}
	if !bytesEqualAt(src, dst, 0, int(n)) {
		return false
	}
	if size > n {
		if !bytesEqualAt(src, dst, size-n, int(n)) {
			return false
		}
	}
	return true
}

func bytesEqualAt(src, dst io.ReaderAt, off int64, n int) bool {
	a := make([]byte, n)
	b := make([]byte, n)
	if err := readFullAt(src, a, off); err != nil {
		return false
	}
	if err := readFullAt(dst, b, off); err != nil {
		return false
	}
	return bytes.Equal(a, b)
}

func readFullAt(r io.ReaderAt, buf []byte, off int64) error {
	got := 0
	for got < len(buf) {
		n, err := r.ReadAt(buf[got:], off+int64(got))
		got += n
		if err != nil {
			if err == io.EOF {
				if got == len(buf) {
					return nil
				}
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func speed(bytes int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) / elapsed.Seconds()
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total) * 100
}
