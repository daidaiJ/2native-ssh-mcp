package manager

import (
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// Foreground exec commands are wrapped so the remote shell records its own
// PID in a temp file. On timeout (or output-limit abort) the whole process
// group is killed over a secondary exec channel; the in-band channel Signal
// stays only as a fallback because OpenSSH frequently ignores it.
//
// The remote shell of an exec channel is its session leader (sshd calls
// setsid per session), so kill -PID targets the entire group including all
// children of the command.

// pidWrapperTimeout is how long killRemoteCommand waits after SIGTERM before
// escalating to SIGKILL.
const pidKillGracePeriod = 2 * time.Second

// buildPIDWrapperScript prefixes the command with a pidfile write of the
// remote shell's own PID; the EXIT trap removes the file on normal exit so
// nothing is left behind.
func buildPIDWrapperScript(command, pidFile string) string {
	return fmt.Sprintf("echo $$ > %s 2>/dev/null; trap 'rm -f %s' EXIT 2>/dev/null; %s",
		shellQuote(pidFile), shellQuote(pidFile), command)
}

// remoteKillScript kills the process group recorded in pidFile. The group
// kill comes first (children die with the leader); a plain PID kill follows
// for remote sshd variants where the shell is not a group leader.
func remoteKillScript(signal, pidFile string) string {
	return fmt.Sprintf("pid=$(cat %s 2>/dev/null); [ -n \"$pid\" ] && { kill -%s -- -\"$pid\" 2>/dev/null; kill -%s \"$pid\" 2>/dev/null; }; exit 0",
		shellQuote(pidFile), signal, signal)
}

// runRemoteScript runs a short script on a secondary exec channel with a hard
// cap so a dying connection cannot stall the kill path.
func runRemoteScript(client *ssh.Client, script string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no ssh client")
	}
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	type outcome struct {
		out []byte
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := sess.CombinedOutput(script)
		done <- outcome{out, err}
	}()
	select {
	case o := <-done:
		return string(o.out), o.err
	case <-time.After(3 * time.Second):
		sess.Close()
		return "", fmt.Errorf("remote script timed out")
	}
}

// killRemoteCommand terminates a foreground exec command after a timeout or
// output-limit abort. Order: in-band SIGTERM (cheap, no round trip), then
// process-group SIGTERM by PID, a short grace period, then SIGKILL for
// anything still alive.
func killRemoteCommand(client *ssh.Client, session *ssh.Session, pidFile string, done <-chan error) {
	if session != nil {
		_ = session.Signal(ssh.SIGTERM)
	}
	if client != nil && pidFile != "" {
		_, _ = runRemoteScript(client, remoteKillScript("TERM", pidFile))
	}
	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(pidKillGracePeriod):
		}
	}
	if session != nil {
		_ = session.Signal(ssh.SIGKILL)
	}
	if client != nil && pidFile != "" {
		_, _ = runRemoteScript(client, remoteKillScript("KILL", pidFile))
	}
}

// remoteCommandContext bundles what the timeout/limit paths need to kill the
// remote process group.
type remoteCommandContext struct {
	client  *ssh.Client
	session *ssh.Session
	pidFile string
	done    chan error
}

func (rc *remoteCommandContext) kill() {
	if rc == nil {
		return
	}
	killRemoteCommand(rc.client, rc.session, rc.pidFile, rc.done)
}
