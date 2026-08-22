package manager

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"ssh-mcp-server-go/internal/config"
)

// TransferResult summarizes a completed file transfer.
type TransferResult struct {
	Action     string        `json:"action"`
	LocalPath  string        `json:"localPath"`
	RemotePath string        `json:"remotePath"`
	Bytes      int64         `json:"bytes"`
	Elapsed    time.Duration `json:"elapsedMs"`
	SpeedBps   float64       `json:"speedBps"`
	Percent    float64       `json:"percent"`
	// Skipped is true when the destination already matches the source
	// (same size and mtime) and the transfer was skipped.
	Skipped bool `json:"skipped,omitempty"`
	// Resumed is true when the transfer continued from an existing partial
	// destination; ResumedFrom is the byte offset it started at.
	Resumed     bool  `json:"resumed,omitempty"`
	ResumedFrom int64 `json:"resumedFrom,omitempty"`
}

// ProgressFunc reports transfer progress. total is -1 when the size is
// unknown.
type ProgressFunc func(done, total int64)

// TransferFile uploads or downloads a file. Transfers are deduplicated
// (skipped when the destination already matches) and resumed from existing
// partial data unless force is set. Progress is reported through the
// callback; the connection is kept alive for the default duration afterwards.
func (m *Manager) TransferFile(action, localPath, remotePath, name string, force bool, progress ProgressFunc) (*TransferResult, error) {
	cfg, err := m.getConfig(name)
	if err != nil {
		return nil, err
	}
	key := m.resolveName(name)
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

	sftpTimeout := time.Duration(cfg.SftpTimeoutMs) * time.Millisecond
	sftpClient, err := m.openSftpWithTimeout(client, sftpTimeout, key)
	if err != nil {
		return nil, err
	}
	defer sftpClient.Close()

	var result *TransferResult
	if action == "upload" {
		result, err = m.upload(sftpClient, validatedLocal, validatedRemote, cfg, force, sftpTimeout, key, progress)
	} else {
		result, err = m.download(sftpClient, validatedRemote, validatedLocal, cfg, force, sftpTimeout, key, progress)
	}
	if err != nil {
		return nil, err
	}

	m.Touch(key, DefaultKeepAliveDuration)
	return result, nil
}

// openSftpWithTimeout opens the SFTP subsystem, bounding the handshake.
func (m *Manager) openSftpWithTimeout(client *ssh.Client, timeout time.Duration, key string) (*sftp.Client, error) {
	// pkg/sftp has no context-aware constructor; bound it with a goroutine.
	type result struct {
		client *sftp.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := sftp.NewClient(client)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("SFTP connection failed: %v", r.err), true)
		}
		return r.client, nil
	case <-time.After(timeout):
		m.Disconnect(key)
		return nil, newToolError(CodeOperationTimeout,
			fmt.Sprintf("SFTP open timed out after %dms", timeout.Milliseconds()), true)
	}
}

// upload copies a local file to the remote server with progress reporting.
// It skips the transfer when the remote file already matches (size + mtime)
// and resumes from the existing remote size when it is a partial copy.
func (m *Manager) upload(sftpClient *sftp.Client, localPath, remotePath string,
	cfg *config.SSHConfig, force bool, timeout time.Duration, key string, progress ProgressFunc) (*TransferResult, error) {

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
	total := localStat.Size()
	localMtime := localStat.ModTime().Unix()

	start := time.Now()
	var done int64
	resumed := false
	var resumedFrom int64

	remoteStat, statErr := sftpClient.Stat(remotePath)
	if statErr == nil && !force {
		remoteSize := remoteStat.Size()
		if remoteSize == total && remoteStat.ModTime().Unix() == localMtime {
			// Dedup: the remote file already matches the local file.
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
		if remoteSize > 0 && remoteSize < total {
			// Resume: continue from the existing partial remote file.
			remoteFile, err := sftpClient.OpenFile(remotePath, os.O_WRONLY)
			if err == nil {
				done, err = copyFile(localFile, remoteFile, remoteSize, total,
					cfg.SftpConcurrency, cfg.SftpChunkSize, offsetProgress(progress, remoteSize, total))
				remoteFile.Close()
				if err != nil {
					return nil, newToolError(CodeSFTPError,
						fmt.Sprintf("File upload failed: %v", err), true)
				}
				resumed = true
				resumedFrom = remoteSize
			}
		}
	}

	if !resumed {
		remoteFile, err := sftpClient.Create(remotePath)
		if err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("File upload failed: %v", err), true)
		}
		done, err = copyFile(localFile, remoteFile, 0, total,
			cfg.SftpConcurrency, cfg.SftpChunkSize, progress)
		remoteFile.Close()
		if err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("File upload failed: %v", err), true)
		}
	}

	// The local file may have grown while transferring; append the tail so
	// both paths stay equivalent for files still being written.
	if err := m.appendUploadTail(sftpClient, localFile, remotePath, total, timeout, key); err != nil {
		return nil, err
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

// download copies a remote file to a temp local file, then renames it into
// place. It skips the transfer when the local file already matches (size +
// mtime) and resumes into the existing local file when it is a partial copy.
func (m *Manager) download(sftpClient *sftp.Client, remotePath, localPath string,
	cfg *config.SSHConfig, force bool, timeout time.Duration, key string, progress ProgressFunc) (*TransferResult, error) {

	remoteStat, err := sftpClient.Stat(remotePath)
	if err != nil {
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
	}
	total := remoteStat.Size()
	remoteMtime := remoteStat.ModTime()

	start := time.Now()

	// Dedup: the local file already matches the remote file.
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

	// Resume: the local file is a partial copy; continue into it.
	if !force {
		if localStat, err := os.Stat(localPath); err == nil && localStat.Size() > 0 && localStat.Size() < total {
			localFile, err := os.OpenFile(localPath, os.O_WRONLY, 0)
			if err == nil {
				remoteFile, err := sftpClient.Open(remotePath)
				if err == nil {
					done, copyErr := copyFile(remoteFile, localFile, localStat.Size(), total,
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
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
	}
	defer remoteFile.Close()

	done, err := copyFile(remoteFile, tempFile, 0, total, cfg.SftpConcurrency, cfg.SftpChunkSize, progress)
	if err != nil {
		cleanup()
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
	}

	// The remote file may have grown while transferring; append the tail.
	if err := m.appendDownloadTail(sftpClient, remoteFile, tempFile, done, timeout, key); err != nil {
		cleanup()
		return nil, err
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
	// Stamp the remote mtime so a later transfer deduplicates.
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

// offsetProgress shifts progress reports by the bytes already present.
func offsetProgress(progress ProgressFunc, offset, total int64) ProgressFunc {
	if progress == nil {
		return nil
	}
	return func(done, _ int64) {
		progress(done+offset, total)
	}
}

// copyFile streams src to dst starting at start, using parallel positional
// reads/writes when the size is known and concurrency is enabled. On
// high-latency links the parallelism keeps multiple SFTP requests in flight,
// which is what makes large transfers usable.
func copyFile(src io.ReaderAt, dst io.WriterAt, start, total int64, workers, chunkSize int, progress ProgressFunc) (int64, error) {
	if total <= 0 || workers <= 1 {
		return copySequential(src, dst, start, total, chunkSize, progress)
	}
	return copyConcurrent(src, dst, start, total, workers, chunkSize, progress)
}

// copySequential copies with positional reads/writes starting at start.
func copySequential(src io.ReaderAt, dst io.WriterAt, start, total int64, chunkSize int, progress ProgressFunc) (int64, error) {
	buf := make([]byte, chunkSize)
	offset := start
	var done int64
	for {
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

// copyConcurrent copies with a fixed worker pool over disjoint offsets.
func copyConcurrent(src io.ReaderAt, dst io.WriterAt, start, total int64, workers, chunkSize int, progress ProgressFunc) (int64, error) {
	var done atomic.Int64
	var firstErr atomic.Value
	var next atomic.Int64
	var wg sync.WaitGroup
	next.Store(start)

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
				n := int64(chunkSize)
				if offset+n > total {
					n = total - offset
				}
				rn, err := src.ReadAt(buf[:n], offset)
				if rn > 0 {
					if _, werr := dst.WriteAt(buf[:rn], offset); werr != nil {
						firstErr.CompareAndSwap(nil, werr)
						return
					}
					done.Add(int64(rn))
					if progress != nil {
						progress(done.Load(), total)
					}
				}
				if err != nil && err != io.EOF {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				if err == io.EOF {
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

// appendUploadTail appends data the local file gained during the transfer.
func (m *Manager) appendUploadTail(sftpClient *sftp.Client, localFile *os.File,
	remotePath string, sizeBefore int64, timeout time.Duration, key string) error {

	currentSize, err := localFile.Stat()
	if err != nil || currentSize.Size() <= sizeBefore {
		return nil
	}
	remoteStat, err := sftpClient.Stat(remotePath)
	if err != nil || remoteStat.Size() >= currentSize.Size() {
		return nil
	}

	remoteFile, err := sftpClient.OpenFile(remotePath, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return newToolError(CodeSFTPError, fmt.Sprintf("File upload failed: %v", err), true)
	}
	defer remoteFile.Close()

	if _, err := copySequential(localFile, remoteFile, remoteStat.Size(), -1, 64*1024, nil); err != nil {
		return newToolError(CodeSFTPError, fmt.Sprintf("File upload failed: %v", err), true)
	}
	return nil
}

// appendDownloadTail appends data the remote file gained during the transfer.
func (m *Manager) appendDownloadTail(sftpClient *sftp.Client, remoteFile *sftp.File,
	tempFile *os.File, downloadedBytes int64, timeout time.Duration, key string) error {

	remoteStat, err := sftpClient.Stat(remoteFile.Name())
	if err != nil || remoteStat.Size() <= downloadedBytes {
		return nil
	}
	if _, err := copySequential(remoteFile, tempFile, downloadedBytes, -1, 64*1024, nil); err != nil {
		return newToolError(CodeSFTPError, fmt.Sprintf("File download failed: %v", err), true)
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