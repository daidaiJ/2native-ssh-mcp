package manager

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestClassifySftpErrorUploadMissingParent(t *testing.T) {
	// pkg/sftp normalises NO_SUCH_FILE to os.ErrNotExist.
	err := classifySftpError("upload", "C:\\local\\file.tar", "/no/such/dir/file.tar", os.ErrNotExist)
	te := AsToolError(err)
	if te.Code != CodeSFTPError {
		t.Fatalf("expected SFTP_ERROR code, got %s", te.Code)
	}
	if !strings.Contains(te.Message, "Remote parent directory does not exist: /no/such/dir") {
		t.Fatalf("upload must report the missing remote parent, got: %s", te.Message)
	}
	if !strings.Contains(te.Message, "local file exists") {
		t.Fatalf("upload message must mention the local file, got: %s", te.Message)
	}
	if te.Retriable {
		t.Fatal("missing parent directory is not retriable as-is")
	}
}

func TestClassifySftpErrorUploadMissingParentStatusError(t *testing.T) {
	// Raw *sftp.StatusError form must classify the same way.
	err := classifySftpError("upload", "C:\\local\\file.tar", "/no/such/dir/file.tar",
		&sftp.StatusError{Code: uint32(sftp.ErrSSHFxNoSuchFile)})
	te := AsToolError(err)
	if !strings.Contains(te.Message, "Remote parent directory does not exist: /no/such/dir") {
		t.Fatalf("upload must report the missing remote parent, got: %s", te.Message)
	}
}

func TestClassifySftpErrorDownloadMissingFile(t *testing.T) {
	err := classifySftpError("download", "C:\\local\\out.tar", "/no/such/file.tar", os.ErrNotExist)
	te := AsToolError(err)
	if !strings.Contains(te.Message, "Remote file does not exist: /no/such/file.tar") {
		t.Fatalf("download must report the missing remote file, got: %s", te.Message)
	}
}

func TestClassifySftpErrorPermissionDenied(t *testing.T) {
	err := classifySftpError("upload", "C:\\local\\f", "/root/secret", os.ErrPermission)
	te := AsToolError(err)
	if !strings.Contains(te.Message, "Remote permission denied: /root/secret") {
		t.Fatalf("permission denied must be reported, got: %s", te.Message)
	}
}

func TestClassifySftpErrorFallback(t *testing.T) {
	err := classifySftpError("upload", "C:\\local\\f", "/remote/f",
		errors.New("sftp: connection lost"))
	te := AsToolError(err)
	if !strings.Contains(te.Message, "File upload failed") ||
		!strings.Contains(te.Message, "local=C:\\local\\f") ||
		!strings.Contains(te.Message, "remote=/remote/f") {
		t.Fatalf("fallback must keep both paths, got: %s", te.Message)
	}
	if !te.Retriable {
		t.Fatal("generic transport failure should be retriable")
	}
}

func TestPosixDir(t *testing.T) {
	cases := map[string]string{
		"/no/such/dir/file.tar": "/no/such/dir",
		"/file.tar":             "/",
		"file.tar":              "/",
	}
	for input, want := range cases {
		if got := posixDir(input); got != want {
			t.Fatalf("posixDir(%q) = %q, want %q", input, got, want)
		}
	}
}