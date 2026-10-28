package tools

import (
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
//   - scriptFile: the local script is prepared for staging (uploaded only
//     after policy checks pass) so execCommand is `sh /tmp/....sh` while
//     auditCommand is the script text itself with a header naming the source
//     and the staged path.
type executeRequest struct {
	execCommand  string
	auditCommand string
	scriptRun    *manager.ScriptRun
	script       *manager.ScriptUpload
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
func resolveExecuteRequest(m *manager.Manager, args map[string]any, sessionName, connectionName string) (*executeRequest, error) {
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
		return resolveScriptRequest(m, req, scriptFile, args, sessionName, connectionName)
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

// resolveScriptRequest reads a local script and prepares the command that
// will run it. The file is not uploaded here: staging happens after the
// approval gate and whitelist/blacklist, so a rejected call never leaves a
// temp file on the remote host.
func resolveScriptRequest(m *manager.Manager, req *executeRequest,
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
	execCommand, auditCommand, run := manager.PrepareScript(scriptFile, content, interpreter, cleanup)
	req.execCommand = execCommand
	req.auditCommand = auditCommand
	req.scriptRun = run
	req.script = &manager.ScriptUpload{RemotePath: run.RemotePath, Content: content}
	// A staged script is parsed by the remote interpreter itself; linting
	// the generated wrapper would duplicate that less precisely.
	req.lintMode = config.LintOff
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
