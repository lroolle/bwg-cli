// Package cli builds the bwg command tree.
//
// Two audiences read this output, and both are first class. A person
// gets aligned tables and coloured severity; an agent gets --json with
// a documented shape, --jq to slice it, errors on stderr with the
// command that fixes them, and exit codes it can branch on without
// parsing text.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lroolle/bwg-cli/internal/config"
	"github.com/lroolle/bwg-cli/internal/fleet"
	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
)

// Exit codes. An agent branches on these instead of matching error
// text, so they are part of the interface and must not drift.
const (
	// ExitOK means the command did what it was asked.
	ExitOK = 0
	// ExitError is any failure not covered by a more specific code.
	ExitError = 1
	// ExitConfig means bwg does not know which server to act on, or has
	// no credentials for it. Fix the configuration and retry.
	ExitConfig = 2
	// ExitRefused means bwg declined to act: read-only mode, or a
	// confirmation that was not given. The request was understood and
	// nothing happened.
	ExitRefused = 3
	// ExitAuth means KiwiVM rejected the credentials.
	ExitAuth = 4
)

// App is the resolved state every command shares: the fleet, the
// global flags, and the streams to write to.
type App struct {
	Cfg *config.Config

	// Global flags.
	ConfigPath  string
	ServerName  string
	JSON        bool
	JQ          string
	ReadOnly    bool
	DryRun      bool
	Yes         bool
	Verbose     bool
	NoColor     bool
	Timeout     time.Duration
	Concurrency int

	Out    io.Writer
	ErrOut io.Writer
	In     io.Reader

	// Version is the build version, for `bwg version` and the User-Agent.
	Version string
}

// NewApp returns an App wired to the process streams.
func NewApp(version string) *App {
	return &App{
		Out: os.Stdout, ErrOut: os.Stderr, In: os.Stdin,
		Version: version, Timeout: kiwivm.DefaultTimeout,
		Concurrency: fleet.DefaultConcurrency,
	}
}

// Init loads the fleet and settles global flags. It runs before every
// command via the root's PersistentPreRunE.
func (a *App) Init() error {
	if a.NoColor {
		output.SetColor(false)
	}
	// The environment can force read-only, but nothing can force it
	// off: a --read-only that a stray variable could clear would be
	// worse than no flag at all.
	if config.EnvReadOnlyRequested() {
		a.ReadOnly = true
	}

	// One line, on stderr, once per run: enough to get the variable
	// renamed, cheap enough not to be worth silencing. It goes away
	// with the variable at v1.0.
	if config.LegacyAPIKeyEnv() {
		a.Notef("%s %s is deprecated — rename it to %s (it still works for now)",
			output.Warn("!"), config.EnvAPIKeyLegacy, config.EnvAPIKey)
	}

	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return err
	}
	a.Cfg = cfg
	return nil
}

// Context returns a context carrying the global timeout.
func (a *App) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if a.Timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, a.Timeout)
}

// Server resolves the server a command should act on, turning the
// config package's errors into ones that name the fix.
func (a *App) Server() (*config.Server, error) {
	s, err := a.Cfg.Resolve(a.ServerName)
	switch {
	case errors.Is(err, config.ErrNoServers):
		return nil, &ExitCodeError{Code: ExitConfig, Err: errors.New(
			"no servers configured\n\n" +
				"  bwg needs two values per VPS, both from the KiwiVM control panel:\n" +
				"    VEID    the VPS ID number (in the panel URL after ?veid=)\n" +
				"    API key under the API tab (looks like private_xxxxxxxx)\n\n" +
				"  Quickest:   export BWG_VEID=<id> BWG_API_KEY=<key>\n" +
				"  Named:      bwg server add <name> --veid <id> --key <api-key>\n" +
				"  Bulk:       bwg server import keys.csv")}
	case errors.Is(err, config.ErrAmbiguous):
		return nil, &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
			"%w\n\n  Pick one:      bwg <command> --server <name>\n"+
				"  Or set a default: bwg server default <name>", err)}
	case errors.Is(err, config.ErrNotFound):
		return nil, &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
			"%w\n\n  Known servers: %s\n  List them:     bwg server ls",
			err, strings.Join(a.Cfg.Names(), ", "))}
	case err != nil:
		return nil, &ExitCodeError{Code: ExitConfig, Err: err}
	}
	return s, nil
}

// Client builds a KiwiVM client for the resolved server.
func (a *App) Client() (*kiwivm.Client, *config.Server, error) {
	s, err := a.Server()
	if err != nil {
		return nil, nil, err
	}
	return a.ClientFor(s), s, nil
}

// ClientForOp is Client for a command that will perform op. It
// refuses a forbidden operation immediately, before the command spends
// round trips on preflight checks whose answer cannot matter.
//
// Without this, a read-only `net ipv6 add` would report "you are at
// your subnet limit" — true, but not the reason nothing happened.
// Commands that mutate should reach for this rather than Client.
func (a *App) ClientForOp(op kiwivm.Op) (*kiwivm.Client, *config.Server, error) {
	if a.ReadOnly && op.Risk > kiwivm.RiskRead {
		return nil, nil, &ExitCodeError{Code: ExitRefused, Err: fmt.Errorf(
			"%s is a %s operation and read-only mode is on\n\n"+
				"  Unset BWG_READ_ONLY or drop --read-only to allow it.",
			op.Endpoint, op.Risk)}
	}
	return a.Client()
}

// ClientFor builds a client for a specific server.
func (a *App) ClientFor(s *config.Server) *kiwivm.Client {
	opts := []kiwivm.Option{
		kiwivm.WithTimeout(a.Timeout),
		kiwivm.WithUserAgent("bwg/" + a.Version + " (+https://github.com/lroolle/bwg-cli)"),
	}
	if a.Verbose {
		opts = append(opts, kiwivm.WithTrace(
			func(method, endpoint string, status int, d time.Duration) {
				fmt.Fprintf(a.ErrOut, "%s %s %s -> %d (%s)\n",
					output.Dim("http"), method, endpoint, status, d.Round(time.Millisecond))
			}))
	}
	return fleet.ClientFor(s, a.ReadOnly, opts...)
}

// Servers returns the fleet a fleet-wide command should sweep,
// honouring --server (one box) and tag filters.
func (a *App) Servers(tags []string) ([]*config.Server, error) {
	if a.ServerName != "" {
		s, err := a.Server()
		if err != nil {
			return nil, err
		}
		return []*config.Server{s}, nil
	}
	all := a.Cfg.Filter(tags)
	if len(all) == 0 {
		if len(a.Cfg.List()) == 0 {
			_, err := a.Server() // reuse the actionable first-run message
			return nil, err
		}
		return nil, &ExitCodeError{Code: ExitConfig, Err: fmt.Errorf(
			"no server matches tag %s\n\n  List them: bwg server ls",
			strings.Join(tags, "+"))}
	}
	return all, nil
}

// Emit writes a command's result: JSON when asked, otherwise the
// human rendering that render() produces.
//
// Every command routes through here so that --json and --jq work
// everywhere without each command remembering to handle them.
func (a *App) Emit(v any, render func(io.Writer)) error {
	if a.JQ != "" {
		return output.JQ(a.Out, v, a.JQ)
	}
	if a.JSON {
		return output.JSON(a.Out, v)
	}
	if render != nil {
		render(a.Out)
		return nil
	}
	return output.JSON(a.Out, v)
}

// Notef writes a note to stderr. Notes never go to stdout: a piped
// consumer must not have to filter commentary out of its data.
func (a *App) Notef(format string, args ...any) {
	fmt.Fprintf(a.ErrOut, format+"\n", args...)
}

// ErrDryRun is returned by Confirm when dry-run mode is on. It is not
// a failure — it means the command validated successfully and would
// have proceeded, but --dry-run told it to stop.
var ErrDryRun = errors.New("dry run — no changes made")

// ExitCodeError carries the process exit code an error should produce.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }
func (e *ExitCodeError) Unwrap() error { return e.Err }

// CodeFor maps an error to its exit code, so `bwg` scripts can branch
// on why something failed without parsing the message.
func CodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrDryRun) {
		return ExitOK
	}
	var coded *ExitCodeError
	if errors.As(err, &coded) {
		return coded.Code
	}
	if kiwivm.IsReadOnly(err) {
		return ExitRefused
	}
	if kiwivm.IsAuth(err) {
		return ExitAuth
	}
	return ExitError
}

// Explain adds the follow-up command for the failures where there is
// an obvious one. An error that names its own fix is the difference
// between an agent recovering and an agent guessing.
func Explain(err error, server string) error {
	if err == nil {
		return nil
	}
	switch {
	case kiwivm.IsReadOnly(err):
		return &ExitCodeError{Code: ExitRefused, Err: fmt.Errorf(
			"%w\n\n  Read-only mode is on. Unset BWG_READ_ONLY or drop --read-only to allow it.", err)}

	case kiwivm.IsAuth(err):
		hint := "  Check the VPS ID (veid) and API key pair — both come from KiwiVM > API.\n" +
			"  KiwiVM reports the same failure for a wrong veid and a wrong key."
		if server != "" {
			hint = fmt.Sprintf("  Show what bwg has: bwg server show %s\n", server) + hint
		}
		return &ExitCodeError{Code: ExitAuth, Err: fmt.Errorf("%w\n\n%s", err, hint)}

	case kiwivm.IsLocked(err):
		return fmt.Errorf("%w\n\n  The VPS is busy with another task. Watch it with: bwg status", err)

	case kiwivm.IsRateLimited(err):
		return fmt.Errorf("%w\n\n  Check the remaining budget: bwg ratelimit", err)

	case kiwivm.IsTransient(err):
		return fmt.Errorf("%w\n\n  This looks temporary — retry shortly.", err)
	}
	return err
}
