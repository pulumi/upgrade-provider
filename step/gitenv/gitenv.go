// Package gitenv provides environment overrides that make git (and the ssh
// it shells out to) fail fast instead of blocking forever on an interactive
// prompt, such as an SSH key passphrase, an SSH host key confirmation, or an
// HTTPS credential prompt.
//
// upgrade-provider has no terminal session available to answer such
// prompts, so a hang here previously looked like the tool freezing
// indefinitely with no explanation. See
// https://github.com/pulumi/upgrade-provider/issues/138.
//
// This logic is shared by both of upgrade-provider's command-execution
// paths (the "step" and "step/v2" packages) so that every network-touching
// git invocation gets the same non-interactive defaults.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// NonInteractive returns env augmented with defaults that make git/ssh fail
// fast instead of hanging on an interactive prompt, unless the caller (or
// the user's own git/ssh configuration) has already opted out explicitly.
//
// If env is nil, the current process environment (os.Environ()) is used as
// the base.
func NonInteractive(ctx context.Context, env []string) []string {
	if env == nil {
		env = os.Environ()
	}

	var hasTerminalPrompt, hasSSHCommand, hasLegacySSH bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="):
			hasTerminalPrompt = true
		case strings.HasPrefix(kv, "GIT_SSH_COMMAND="):
			hasSSHCommand = true
		case strings.HasPrefix(kv, "GIT_SSH="):
			hasLegacySSH = true
		}
	}

	if !hasTerminalPrompt {
		// Disable git's own prompts for usernames/passwords (e.g. over HTTPS).
		env = append(env, "GIT_TERMINAL_PROMPT=0")
	}

	if !hasSSHCommand {
		switch {
		case hasLegacySSH:
			// GIT_SSH_COMMAND takes precedence over both core.sshCommand and
			// the legacy GIT_SSH, and GIT_SSH accepts no extra arguments, so
			// there is no way to layer -oBatchMode=yes onto it without
			// discarding the user's custom ssh program entirely. Leave
			// their configuration alone rather than silently overriding it.
		default:
			if sshCommand := coreSSHCommand(ctx); sshCommand != "" {
				// core.sshCommand is also lower precedence than
				// GIT_SSH_COMMAND, so setting our own default
				// unconditionally would silently discard it too. Preserve
				// the user's command and just append BatchMode so it still
				// fails fast instead of prompting.
				env = append(env, "GIT_SSH_COMMAND="+sshCommand+" -oBatchMode=yes")
			} else {
				// Disable ssh's prompts for passphrases, host key
				// confirmation, etc. so that authentication failures return
				// immediately instead of blocking on input that will never
				// come.
				env = append(env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
			}
		}
	}

	return env
}

// coreSSHCommand returns the effective value of git's core.sshCommand
// configuration, or "" if it is unset or cannot be determined.
func coreSSHCommand(ctx context.Context) string {
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := exec.CommandContext(ctx, "git", "config", "--get", "core.sshCommand").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
