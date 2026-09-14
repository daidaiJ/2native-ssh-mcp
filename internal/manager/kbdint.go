package manager

import (
	"context"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

// kbdintPromptTimeout bounds one human prompt for the non-password
// keyboard-interactive questions (OTP and similar challenges). A constant on
// purpose for now; connection-level tuning only if someone actually needs it.
var kbdintPromptTimeout = 2 * time.Minute

// Challenge describes one keyboard-interactive round: only the non-password
// questions (the server's OTP/challenge prompts) are forwarded to the human.
type Challenge struct {
	// Connection is the connection name the prompt belongs to.
	Connection string
	// Name and Instruction are the raw SSH server strings.
	Name        string
	Instruction string
	// Questions are the challenge texts, e.g. "Verification code:".
	Questions []string
	// Echos mirrors the server's echo flags: false means the answer should
	// be rendered as a secret (password field).
	Echos []bool
}

// Elicitor prompts the human for the non-password keyboard-interactive
// questions. Implementations live in the tools layer (MCP elicitation); the
// manager only sees this interface so it never imports the MCP server.
//
// Error semantics are informational only: the keyboard-interactive callback
// never aborts the handshake because a prompt failed — it degrades to the
// SSH_MCP_2FA_CODE fallback (or empty answers) instead, so a client without
// elicitation support or an unanswered prompt cannot wedge or kill the
// authentication path.
type Elicitor interface {
	// Prompt receives only the non-password questions and must return
	// answers of the same length as ch.Questions.
	Prompt(ctx context.Context, ch Challenge) (answers []string, err error)
}

// elicitorKeyType is the context key used to carry the Elicitor from the
// tool call that triggered a dial into the SSH handshake. Per-request
// injection keeps the OTP prompt on the MCP session that caused the dial —
// never broadcast to other clients of the daemon.
type elicitorKeyType struct{}

// WithElicitor returns a context that carries the Elicitor for any dial
// started from it.
func WithElicitor(ctx context.Context, e Elicitor) context.Context {
	return context.WithValue(ctx, elicitorKeyType{}, e)
}

func elicitorFromContext(ctx context.Context) Elicitor {
	if ctx == nil {
		return nil
	}
	e, _ := ctx.Value(elicitorKeyType{}).(Elicitor)
	return e
}

// keyboardInteractive answers keyboard-interactive prompts. Priority:
//
//  1. password questions ("password"/"密码") use the configured password;
//  2. other questions go to the Elicitor (one prompt for all of them) when
//     one is attached to the dial's context — the SSH_MCP_2FA_CODE env var
//     is deliberately ignored while the prompt path is available;
//  3. anything the prompt path could not answer (no elicitor, declined,
//     cancelled, timed out, unsupported client) falls back to the env var,
//     else to empty answers.
//
// The callback never returns an error: failing the prompt must not abort the
// handshake (the server simply rejects the wrong/empty answers through its
// normal flow, and any other configured auth method still gets its turn).
func keyboardInteractive(cfg *config.SSHConfig, elicitor Elicitor, ctx context.Context) ssh.KeyboardInteractiveChallenge {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		envCode := os.Getenv("SSH_MCP_2FA_CODE")
		answers := make([]string, len(questions))
		var challengeIdx []int
		for i, q := range questions {
			lower := strings.ToLower(q)
			if strings.Contains(lower, "password") || strings.Contains(lower, "密码") {
				answers[i] = cfg.Password
			} else {
				challengeIdx = append(challengeIdx, i)
			}
		}
		if len(challengeIdx) == 0 {
			logger.Info("keyboard-interactive challenge for [%s], %d questions (password only)", cfg.Name, len(questions))
			return answers, nil
		}
		logger.Info("keyboard-interactive challenge for [%s], %d questions", cfg.Name, len(questions))

		if elicitor != nil {
			promptCtx := ctx
			if promptCtx == nil {
				promptCtx = context.Background()
			}
			promptCtx, cancel := context.WithTimeout(promptCtx, kbdintPromptTimeout)
			ch := Challenge{
				Connection:  cfg.Name,
				Name:        name,
				Instruction: instruction,
				Questions:   questionsOf(questions, challengeIdx),
				Echos:       echosOf(echos, challengeIdx),
			}
			challengeAnswers, err := elicitor.Prompt(promptCtx, ch)
			cancel()
			if err == nil && len(challengeAnswers) == len(challengeIdx) {
				for k, i := range challengeIdx {
					answers[i] = challengeAnswers[k]
				}
				return answers, nil
			}
			// Fail-open: declined, cancelled, timed out, unsupported client
			// or a malformed answer list all degrade to the env fallback
			// below instead of aborting the handshake.
		}
		if envCode != "" {
			for _, i := range challengeIdx {
				answers[i] = envCode
			}
		}
		return answers, nil
	}
}

// questionsOf picks the challenge questions by their original indices.
func questionsOf(questions []string, idx []int) []string {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, questions[i])
	}
	return out
}

// echosOf picks the echo flags by their original indices (false when the
// server sent fewer flags than questions).
func echosOf(echos []bool, idx []int) []bool {
	out := make([]bool, 0, len(idx))
	for _, i := range idx {
		if i < len(echos) {
			out = append(out, echos[i])
		} else {
			out = append(out, false)
		}
	}
	return out
}

// RecordingElicitor is a test double: it records every prompt and replays
// canned answers or a canned error.
type RecordingElicitor struct {
	// Challenges collects one entry per Prompt call.
	Challenges []Challenge
	// Answers is returned verbatim when Err is nil.
	Answers []string
	// Err, when set, is returned instead of Answers.
	Err error
	// Delay blocks every Prompt for the given duration before answering.
	Delay time.Duration
}

// Prompt implements Elicitor.
func (r *RecordingElicitor) Prompt(ctx context.Context, ch Challenge) ([]string, error) {
	r.Challenges = append(r.Challenges, ch)
	if r.Delay > 0 {
		select {
		case <-time.After(r.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Answers, nil
}
