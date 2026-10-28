package manager

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
)

// maxScriptFileBytes caps a local script handed to scriptFile. A file larger
// than this is not a command, and its content would stop being readable in the
// audit record.
const maxScriptFileBytes = 1 << 20 // 1 MiB

// scriptAuditMaxBytes caps the script text copied into the audit record. The
// record states the total size when it clips, so a reviewer always knows the
// audit copy is shorter than what ran.
const scriptAuditMaxBytes = 16 * 1024

// scriptTempPrefix matches the background-session temp files: everything this
// server stages in /tmp carries one recognizable prefix.
const scriptTempPrefix = "/tmp/.2native-ssh-mcp-"

// ScriptRun describes a scriptFile execution: where the local script was
// staged, what ran it, and whether the temp file was removed afterwards.
type ScriptRun struct {
	LocalPath   string `json:"localPath"`
	RemotePath  string `json:"remotePath"`
	Interpreter string `json:"interpreter"`
	Bytes       int    `json:"bytes"`
	Cleanup     bool   `json:"cleanup"`
	Shebang     bool   `json:"shebang,omitempty"`
}

// Summary is the one-line description of a staged-script run, shown in the
// command result and in the background-start response.
func (s ScriptRun) Summary() string {
	runner := s.Interpreter
	if s.Shebang {
		runner = "its own shebang"
	}
	cleaned := "removed after the run (an interrupted run may leave it behind)"
	if !s.Cleanup {
		cleaned = "left in place (scriptCleanup=false)"
	}
	return fmt.Sprintf("[scriptFile] %s (%d bytes) staged to %s, run by %s, %s",
		s.LocalPath, s.Bytes, s.RemotePath, runner, cleaned)
}

// ReadScriptFile reads a local script for a scriptFile execution, applying the
// same localPathMode rules as file-transfer (the source is read, so it is
// validated as a read path and must exist).
func (m *Manager) ReadScriptFile(localPath, connectionName string) ([]byte, error) {
	resolved, err := m.validateLocalPath(localPath, connectionName, "read")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Failed to read local script: %v", err), false)
	}
	if info.IsDir() {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("scriptFile must be a file, not a directory: %s", localPath), false)
	}
	if info.Size() > maxScriptFileBytes {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("scriptFile is %d bytes; the limit is %d bytes. Split the script or run it with cmdString instead.",
				info.Size(), maxScriptFileBytes), false)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("Failed to read local script: %v", err), false)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, newToolError(CodeLocalFileReadFailed,
			fmt.Sprintf("scriptFile is empty: %s", localPath), false)
	}
	return content, nil
}

// StageScript uploads a script to the connection's remote /tmp under a
// one-time random name (atomic: <target>.part then rename, mode 0700) and
// returns the remote path. Nothing about the upload is hidden from the audit
// trail: the record for the run carries the script text itself.
func (m *Manager) StageScript(ctx context.Context, connectionName, localPath string, content []byte) (string, error) {
	key := m.resolveName(connectionName)
	client, err := m.EnsureConnected(ctx, key)
	if err != nil {
		return "", err
	}
	cfg := m.configs[key]
	sftpClient, sshClient, err := m.acquireSftp(ctx, client, key, cfg, false)
	if err != nil {
		return "", err
	}
	remotePath := scriptTempPath(localPath)
	partPath := remotePath + uploadPartSuffix
	if err := writeRemoteFile(sftpClient, partPath, remotePath, content); err != nil {
		_ = sftpClient.Remove(partPath)
		// A failed upload leaves the pooled client in an unknown state for
		// the next user: close it instead of returning it to the pool.
		_ = sftpClient.Close()
		return "", classifySftpError("upload", localPath, remotePath, err)
	}
	m.releaseSftp(key, cfg, false, sftpClient, sshClient)
	return remotePath, nil
}

// writeRemoteFile writes content to partPath, makes it executable and moves it
// onto remotePath, so a reader never sees a half-written script.
func writeRemoteFile(sftpClient *sftp.Client, partPath, remotePath string, content []byte) error {
	f, err := sftpClient.Create(partPath)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := sftpClient.Chmod(partPath, 0o700); err != nil {
		return err
	}
	return sftpClient.Rename(partPath, remotePath)
}

// ScriptCommand builds the command that runs a staged script and, when
// cleanup is on, removes the temp file afterwards without losing the exit
// code: `(exit $rc)` in a subshell leaves the script's status as the status of
// the whole line, so a background job's `echo $? > exitfile` still records the
// script's own exit code.
func ScriptCommand(remotePath string, content []byte, interpreter string, cleanup bool) string {
	run := shellQuote(remotePath)
	if !scriptHasShebang(content) {
		run = interpreter + " " + run
	}
	if !cleanup {
		return run
	}
	return run + "; __mcp_script_rc=$?; rm -f " + shellQuote(remotePath) + "; (exit $__mcp_script_rc)"
}

// ScriptAuditText renders the readable equivalent of a staged-script run for
// the audit record and the approval prompt: the header names the source, the
// staged path, the interpreter and what happens to the staged file, and the
// body is the script text itself — the same commands the remote host executes,
// with no encoding in between.
func ScriptAuditText(localPath, remotePath, interpreter string, content []byte, cleanup bool) string {
	runner := interpreter
	if scriptHasShebang(content) {
		runner = "its own shebang"
	}
	header := fmt.Sprintf("# scriptFile %s staged to %s, run by %s", localPath, remotePath, runner)
	if cleanup {
		header += "; the staged file is removed afterwards"
	} else {
		header += "; the staged file is left in place (scriptCleanup=false)"
	}
	body := content
	if len(body) > scriptAuditMaxBytes {
		body = append(append([]byte{}, body[:scriptAuditMaxBytes]...),
			[]byte(fmt.Sprintf("\n# ... [audit copy truncated: the script is %d bytes in total]\n", len(content)))...)
	}
	return header + "\n" + string(body)
}

// scriptHasShebang reports whether the script declares its own interpreter;
// such a script runs directly, everything else runs through scriptInterpreter.
func scriptHasShebang(content []byte) bool {
	return bytes.HasPrefix(content, []byte("#!"))
}

// scriptTempPath builds the remote temp path for a staged script: the shared
// /tmp/.2native-ssh-mcp- prefix, the local file's base name for legibility in
// `ps` and the audit record, and a one-time random id so a predictable path
// cannot be symlink-preempted by another user.
func scriptTempPath(localPath string) string {
	return fmt.Sprintf("%s%s-%s.sh", scriptTempPrefix,
		sanitizeScriptName(filepath.Base(localPath)), randomID("script"))
}

// sanitizeScriptName reduces a local file name to a short path segment of
// characters that are safe in a remote path. Any directory part is dropped
// first, so the result can never steer the staging path.
func sanitizeScriptName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		out = "script"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
