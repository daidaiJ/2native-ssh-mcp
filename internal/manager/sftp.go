package manager

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

const resumeSampleBytes = 32 * 1024

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
func (m *Manager) TransferFile(ctx context.Context, action, localPath, remotePath, name string, force bool, progress ProgressFunc) (*TransferResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	tctx, cancel := context.WithTimeout(ctx, sftpTimeout)
	defer cancel()

	sftpClient, err := m.openSftp(tctx, client, key)
	if err != nil {
		return nil, err
	}
	defer sftpClient.Close()
	stop := context.AfterFunc(tctx, func() { _ = sftpClient.Close() })
	defer stop()

	var result *TransferResult
	if action == "upload" {
		result, err = m.upload(tctx, sftpClient, validatedLocal, validatedRemote, cfg, force, progress)
	} else {
		result, err = m.download(tctx, sftpClient, validatedRemote, validatedLocal, cfg, force, progress)
	}
	if err != nil {
		if tctx.Err() != nil {
			return nil, ctxToolError(tctx, CodeOperationTimeout,
				fmt.Sprintf("File transfer timed out after %dms", sftpTimeout.Milliseconds()))
		}
		return nil, err
	}

	m.Touch(key, DefaultKeepAliveDuration)
	return result, nil
}

// openSftp opens the SFTP subsystem, bounded by ctx.
func (m *Manager) openSftp(ctx context.Context, client *ssh.Client, key string) (*sftp.Client, error) {
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
	case <-ctx.Done():
		m.Disconnect(key)
		return nil, ctxToolError(ctx, CodeOperationTimeout, "SFTP open timed out")
	}
}

func (m *Manager) upload(ctx context.Context, sftpClient *sftp.Client, localPath, remotePath string,
	cfg *config.SSHConfig, force bool, progress ProgressFunc) (*TransferResult, error) {

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
	localMtime := localStat.ModTime()

	start := time.Now()
	var done int64
	resumed := false
	var resumedFrom int64

	remoteStat, statErr := sftpClient.Stat(remotePath)
	if statErr == nil && !force {
		remoteSize := remoteStat.Size()
		if remoteSize == total && remoteStat.ModTime().Unix() == localMtime.Unix() {
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
			if remoteRO, err := sftpClient.Open(remotePath); err == nil {
				match := overlapMatches(localFile, remoteRO, remoteSize)
				remoteRO.Close()
				if match {
					remoteFile, err := sftpClient.OpenFile(remotePath, os.O_WRONLY)
					if err == nil {
						done, err = copyFile(ctx, localFile, remoteFile, remoteSize, total,
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
		}
	}

	if !resumed {
		remoteFile, err := sftpClient.Create(remotePath)
		if err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("File upload failed: %v", err), true)
		}
		done, err = copyFile(ctx, localFile, remoteFile, 0, total,
			cfg.SftpConcurrency, cfg.SftpChunkSize, progress)
		remoteFile.Close()
		if err != nil {
			return nil, newToolError(CodeSFTPError,
				fmt.Sprintf("File upload failed: %v", err), true)
		}
	}

	tail, err := m.appendUploadTail(ctx, sftpClient, localFile, remotePath, total)
	if err != nil {
		return nil, err
	}
	done += tail

	if st, err := localFile.Stat(); err == nil {
		_ = sftpClient.Chtimes(remotePath, st.ModTime(), st.ModTime())
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
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
	}
	total := remoteStat.Size()
	remoteMtime := remoteStat.ModTime()

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
		return nil, newToolError(CodeSFTPError,
			fmt.Sprintf("File download failed: %v", err), true)
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
	if total <= 0 || workers <= 1 {
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
