package tools

import (
	"context"
	"fmt"
	"strings"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// executeRequest is a fully resolved execute-command call. Three argument
// forms land here and stay distinguishable downstream, because the audit
// trail must show a command a human can read:
//
//   - cmdString (shell mode): executed as written; execCommand is the text and
//     auditCommand stays empty (the text is already readable).
//   - cmdString + args (argv mode): every argument is quoted by the server, so
//     execCommand is quoted text while auditCommand is the same command line
//     with only the quoting a shell actually needs.
//   - scriptFile: the local script is staged to the remote host and run, so
//     execCommand is `sh /tmp/....sh` while auditCommand is the script text
//     itself with a header naming the source and the staged path.
type executeRequest struct {
	execCommand  string
	auditCommand string
	scriptRun    *manager.ScriptRun
	lintMode     string
}

// gateText is the text the approval gate classifies and shows the human: the
// readable equivalent when there is one, otherwise the command itself.
func (r *executeRequest) gateText() string {
	if strings.TrimSpace(r.auditCommand) != "" {
		return r.auditCommand
	}
	return r.execCommand
}

// resolveExecuteRequest validates the execute-command arguments and prepares
// everything that must happen before the command is gated and run.
func resolveExecuteRequest(ctx context.Context, m *manager.Manager, args map[string]any, sessionName, connectionName string) (*executeRequest, error) {
	cmdString, _ := args["cmdString"].(string)
	scriptFile, _ := args["scriptFile"].(string)
	lintMode, _ := args["lintMode"].(string)
	argvArgs, err := stringArrayArg(args["args"])
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(scriptFile) != "" && strings.TrimSpace(cmdString) != "" {
		return nil, manager.NewToolError(manager.CodeCommandValidationFailed,
			"cmdString and scriptFile are mutually exclusive: pass the script through scriptFile, or the command through cmdString", false)
	}
	if strings.TrimSpace(scriptFile) == "" && strings.TrimSpace(cmdString) == "" {
		return nil, manager.NewToolError(manager.CodeCommandValidationFailed,
			"cmdString (or scriptFile) must be a non-empty command", false)
	}
	if strings.TrimSpace(scriptFile) != "" && len(argvArgs) > 0 {
		return nil, manager.NewToolError(manager.CodeCommandValidationFailed,
			"args cannot be combined with scriptFile: the script's arguments belong inside the script", false)
	}
	req := &executeRequest{lintMode: lintMode}
	switch {
	case strings.TrimSpace(scriptFile) != "":
		return resolveScriptRequest(ctx, m, req, scriptFile, args, sessionName, connectionName)
	case len(argvArgs) > 0:
		execText, auditText, err := manager.JoinArgv(cmdString, argvArgs)
		if err != nil {
			return nil, err
		}
		req.execCommand = execText
		req.auditCommand = auditText
		// argv mode has no shell syntax left to lint: the server quoted every
		// argument, so there is nothing an inner shell could re-parse.
		req.lintMode = config.LintOff
	default:
		req.execCommand = cmdString
	}
	return req, nil
}

// resolveScriptRequest stages a local script on the remote host and fills the
// request with the command that runs it plus the audit-visible equivalent.
func resolveScriptRequest(ctx context.Context, m *manager.Manager, req *executeRequest,
	scriptFile string, args map[string]any, sessionName, connectionName string) (*executeRequest, error) {
	key, err := m.ResolveConnection(sessionName, connectionName)
	if err != nil {
		return nil, err
	}
	content, err := m.ReadScriptFile(scriptFile, key)
	if err != nil {
		return nil, err
	}
	cfg := m.ConfigFor(sessionName, connectionName)
	interpreter := cfg.GetScriptInterpreter()
	cleanup := cfg.GetScriptCleanup()
	if v, ok := args["scriptCleanup"].(bool); ok {
		cleanup = v
	}
	remotePath, err := m.StageScript(ctx, key, scriptFile, content)
	if err != nil {
		return nil, err
	}
	req.execCommand = manager.ScriptCommand(remotePath, content, interpreter, cleanup)
	req.auditCommand = manager.ScriptAuditText(scriptFile, remotePath, interpreter, content, cleanup)
	req.scriptRun = &manager.ScriptRun{
		LocalPath:   scriptFile,
		RemotePath:  remotePath,
		Interpreter: interpreter,
		Bytes:       len(content),
		Cleanup:     cleanup,
		Shebang:     strings.HasPrefix(string(content), "#!"),
	}
	return req, nil
}

// stringArrayArg reads an array-of-strings tool argument. Elements that are
// not strings are rejected instead of being dropped, so a mistyped call never
// silently runs something else.
func stringArrayArg(v any) ([]string, error) {
	switch items := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return items, nil
	case []any:
		out := make([]string, 0, len(items))
		for i, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, manager.NewToolError(manager.CodeCommandValidationFailed,
					fmt.Sprintf("args[%d] must be a string", i), false)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, manager.NewToolError(manager.CodeCommandValidationFailed,
			"args must be an array of strings", false)
	}
}
