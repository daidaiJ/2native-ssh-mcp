// Package approval classifies commands for the destructive-command approval
// gate and drives the MCP elicitation round-trip.
//
// Classification is deliberately conservative on the "ask" side only: it is
// a best-effort safety net, not a policy engine. A command the classifier
// misses still runs (subject to the connection's commandWhitelist/
// commandBlacklist); users who know their box can extend or relax the
// classification via config.ApprovalPatterns / ApprovalExemptPatterns.
package approval

import (
	"fmt"
	"regexp"
	"strings"
)

// Mode is the effective approval mode for a connection.
type Mode string

const (
	// ModeAuto never asks (default; the pre-approval-gate behavior).
	ModeAuto Mode = "auto"
	// ModeAskDestructive asks via elicitation before destructive commands.
	ModeAskDestructive Mode = "ask-destructive"
)

// invocation is one startable word after wrapper stripping, matched on the
// segment's first token.
type invocation struct {
	// name is the first token (without arguments) that triggers the rule.
	name string
	// argRe optionally constrains the arguments (e.g. systemctl's action
	// verb). nil means any invocation of the binary is destructive.
	argRe *regexp.Regexp
	// reason is shown to the human in the elicitation message.
	reason string
}

// builtinInvocations lists binaries whose plain invocation destroys data or
// takes the host down. `mv` is intentionally absent (routine); `kill`/`pkill`
// are included because they terminate running work, and ask-destructive is
// opt-in per connection so the prompt cost is the operator's choice.
var builtinInvocations = []invocation{
	{name: "rm", reason: "rm deletes files"},
	{name: "shred", reason: "shred destroys file contents"},
	{name: "mkfs", reason: "mkfs formats a filesystem"},
	{name: "mkfs.ext2", reason: "mkfs formats a filesystem"},
	{name: "mkfs.ext3", reason: "mkfs formats a filesystem"},
	{name: "mkfs.ext4", reason: "mkfs formats a filesystem"},
	{name: "mkfs.xfs", reason: "mkfs formats a filesystem"},
	{name: "mkfs.btrfs", reason: "mkfs formats a filesystem"},
	{name: "mkfs.vfat", reason: "mkfs formats a filesystem"},
	{name: "fdisk", reason: "fdisk edits partition tables"},
	{name: "sfdisk", reason: "sfdisk edits partition tables"},
	{name: "parted", reason: "parted edits partition tables"},
	{name: "wipefs", reason: "wipefs erases filesystem signatures"},
	{name: "shutdown", reason: "shutdown takes the host down"},
	{name: "reboot", reason: "reboot restarts the host"},
	{name: "halt", reason: "halt stops the host"},
	{name: "poweroff", reason: "poweroff takes the host down"},
	{name: "init", argRe: regexp.MustCompile(`^\s*[06]\s*$`), reason: "init 0/6 stops or reboots the host"},
	{name: "userdel", reason: "userdel deletes an account"},
	{name: "groupdel", reason: "groupdel deletes a group"},
	{name: "kill", reason: "kill terminates processes"},
	{name: "pkill", reason: "pkill terminates processes"},
	{name: "killall", reason: "killall terminates processes"},
	{name: "crontab", argRe: regexp.MustCompile(`^-r\b`), reason: "crontab -r removes the crontab"},
	{name: "chmod", argRe: regexp.MustCompile(`^-[a-zA-Z]*[Rr][a-zA-Z]*\s`), reason: "chmod -R changes a whole tree"},
	{name: "chown", argRe: regexp.MustCompile(`^-[a-zA-Z]*[Rr][a-zA-Z]*\s`), reason: "chown -R changes a whole tree"},
}

// actionMultiplexers are binaries that take the dangerous action as an
// argument (systemctl reboot, service nginx stop).
var actionMultiplexers = map[string]*regexp.Regexp{
	"systemctl": regexp.MustCompile(
		`^(stop|restart|try-restart|disable|mask|reboot|poweroff|halt|isolate|kill)\b`),
	"service": regexp.MustCompile(
		`^\S+\s+(stop|restart)\b`),
	"nft": regexp.MustCompile(`^flush\b`),
}

// builtinPatterns match anywhere in the command: constructs whose dangerous
// part is an argument or redirect target rather than an invocation. All are
// linear; a restart-at-every-offset regex here was the shape of a ReDoS in
// the reference implementation this list draws from, so nothing backtracks.
var builtinPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork bomb"},
	{regexp.MustCompile(`\biptables\s+-F\b`), "iptables -F flushes the firewall"},
	{regexp.MustCompile(`\bufw\s+reset\b`), "ufw reset clears the firewall"},
	{regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|vd)`), "redirect overwrites a raw disk device"},
	{regexp.MustCompile(`>\s*/etc/(passwd|shadow|sudoers|fstab|cron|systemd)`), "redirect overwrites a critical system file"},
	{regexp.MustCompile(`>\s*\S*authorized_keys\b`), "redirect overwrites authorized_keys"},
	{regexp.MustCompile(`\bdd\b[^;|&]*\bof=/dev/`), "dd writes to a raw device"},
	{regexp.MustCompile(`(?i)\b(drop\s+(table|database)|truncate\s+table)\b`), "SQL drop/truncate destroys data"},
	{regexp.MustCompile(`\bgit\s+push\b[^;|&]*--force\b`), "git push --force overwrites remote history"},
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "git reset --hard discards changes"},
	{regexp.MustCompile(`(?i)\bdel\s+/[sq]|\brd\s+/s\b|\bformat\b\s+[a-zA-Z]:`), "Windows destructive command"},
	{regexp.MustCompile(`(?i)\bremove-item\b[^;|&]*-recurse\b[^;|&]*-force\b`), "PowerShell Remove-Item -Recurse -Force deletes a tree"},
}

// wrappers are binaries that execute their arguments: stripping them from the
// front of a segment lets the real invocation surface (sudo rm -rf /, nice
// reboot, env dd of=...). env is stripped unconditionally, which misclassifies
// nothing: env with a destructive payload is destructive anyway.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "nice": true,
	"stdbuf": true, "time": true, "command": true, "timeout": true,
	"setsid": true, "xargs": true, "eval": true,
}

// segmentSplitters cut a command into independently startable pieces. This is
// not a shell parser: `$(` and backticks survive as leading junk on the inner
// segment and get trimmed below, which covers the common nested cases without
// pretending to understand quoting.
var segmentSplitter = func() *regexp.Regexp {
	seps := []string{"&&", "||", ";", "|", "\n", "&", "(", ")"}
	quoted := make([]string, len(seps))
	for i, s := range seps {
		quoted[i] = regexp.QuoteMeta(s)
	}
	return regexp.MustCompile(`(?:` + strings.Join(quoted, "|") + `)`)
}()

// IsDestructive reports whether cmd should trigger the approval gate for a
// connection. extra are user regexes that widen the destructive set; exempt
// are user regexes that force a command through without asking (they win over
// everything, including the built-ins — the operator owns that trade). The
// second return value is a human-readable reason for the elicitation message.
func IsDestructive(cmd string, extra, exempt []string) (bool, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false, ""
	}
	for _, p := range exempt {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(cmd) {
			return false, ""
		}
	}
	for _, inv := range builtinInvocations {
		if name, args, ok := leadingInvocation(cmd, inv.name); ok {
			if inv.argRe == nil || inv.argRe.MatchString(args) {
				return true, reasonFor(name, inv.reason)
			}
		}
	}
	for name, argRe := range actionMultiplexers {
		if _, args, ok := leadingInvocation(cmd, name); ok {
			if argRe.MatchString(args) {
				return true, fmt.Sprintf("%s %s", name, strings.TrimSpace(args))
			}
		}
	}
	for _, p := range builtinPatterns {
		if p.re.MatchString(cmd) {
			return true, p.reason
		}
	}
	for _, p := range extra {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(cmd) {
			return true, fmt.Sprintf("matches approvalPatterns entry %q", p)
		}
	}
	return false, ""
}

// leadingInvocation reports whether any segment of cmd starts with the named
// binary (after wrapper stripping), returning the first matching token and
// its arguments. Segments come from splitting on shell separators; leading
// shell junk from command substitution is trimmed so `$(rm -rf /)` and
// "`reboot`" classify like their bare forms.
func leadingInvocation(cmd, name string) (string, string, bool) {
	for _, seg := range segmentSplitter.Split(cmd, -1) {
		fields := strings.Fields(strings.Trim(seg, "$({`} \t"))
		for len(fields) > 0 && wrappers[fields[0]] {
			// `timeout 30 reboot` carries the wrapper's own argument first.
			if fields[0] == "timeout" && len(fields) > 2 {
				fields = fields[2:]
				continue
			}
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		if fields[0] == name {
			return name, strings.Join(fields[1:], " "), true
		}
	}
	return "", "", false
}

func reasonFor(name, reason string) string {
	return fmt.Sprintf("%s: %s", name, reason)
}
