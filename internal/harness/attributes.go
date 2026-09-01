package harness

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The resource attributes Mirador stamps on everything a harness emits. These are the
// join keys: without service.name a trace cannot be filtered to one harness, and
// without enduser.id a team's traces cannot be told apart.
const (
	// AttrServiceName is what `mirador trace list --filter 'service.name="claude-code"'`
	// matches on. Set explicitly rather than relying on the harness's default, so the
	// documented filter keeps working if that default ever changes.
	AttrServiceName = "service.name"

	// AttrEnduserID attributes a trace to a person. Resolved from git's configured
	// email, which is the identity already attached to the work being traced.
	AttrEnduserID = "enduser.id"

	// AttrProjectID records which Mirador project the harness was connected against, so
	// `telemetry status` can report the project the harness actually reports to rather
	// than whichever one happens to be selected now.
	AttrProjectID = "mirador.project.id"
)

// ServiceName is the service.name a harness reports under. Derived from the harness
// token so `claude` and `codex` are distinguishable in one project.
func ServiceName(h Harness) string {
	switch h.Name() {
	case "claude":
		return "claude-code"
	default:
		return h.Name()
	}
}

// GitEmail returns the email git would stamp on a commit here, or "" when git is
// absent or unconfigured.
//
// This is read at connect time and written into the config as a literal. It cannot be
// left as a shell substitution: harness config files hold literal strings, and a
// `$(git config user.email)` written into one would be exported verbatim as that text.
func GitEmail(ctx context.Context) string {
	path, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "config", "--get", "user.email").Output()
	if err != nil {
		// Unset is the ordinary case (exit 1), not a failure worth reporting.
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Hostname is the fallback identity when git has no email configured, and the default
// suffix for a minted key's name.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown-host"
	}
	// A hostname like "work-laptop.local" reads better trimmed to its first label.
	if short, _, found := strings.Cut(name, "."); found && short != "" {
		return short
	}
	return name
}
