package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/lroolle/bwg-cli/kiwivm"
	"github.com/lroolle/bwg-cli/pkg/output"
	"github.com/spf13/cobra"
)

func newSSHCmd(app *App) *cobra.Command {
	var (
		printOnly   bool
		user        string
		useIPv6     bool
		noHostCheck bool
		identity    string
	)

	cmd := &cobra.Command{
		Use:   "ssh [-- ssh-args...]",
		Short: "SSH into a server, resolving its address and port from the API",
		Long: `Open an SSH session to a VPS.

The point of this over plain ssh is that KiwiVM knows the real SSH
port, which is frequently not 22 and changes on reinstall. bwg asks
the API, then execs your ssh client — it does not implement SSH
itself, so your keys, agent and ~/.ssh/config all still apply.

Anything after -- is passed through to ssh.`,
		Example: `  bwg ssh
  bwg ssh --server tokyo
  bwg ssh --ipv6                         # connect via IPv6
  bwg ssh --identity ~/.ssh/id_ed25519   # specific key
  bwg ssh --no-host-check                # skip known_hosts (after reinstall)
  bwg ssh -- -A -L 8080:localhost:80     # forwarded through to ssh
  bwg ssh -- uptime                      # run one command
  bwg ssh --print                        # just print the command`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			live, err := c.LiveServiceInfo(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}

			host := live.PrimaryIP()
			if useIPv6 {
				if v6 := live.IPv6(); len(v6) > 0 {
					host = v6[0]
					if idx := strings.Index(host, "/"); idx >= 0 {
						host = host[:idx]
					}
				} else {
					return fmt.Errorf("%s has no IPv6 address\n\n"+
						"  Assign one: bwg net ipv6 add", s.Name)
				}
			}
			if host == "" {
				return fmt.Errorf("%s has no IP address assigned", s.Name)
			}

			port := s.SSHPort
			if port == 0 {
				port = live.SSHPort.Int()
			}
			if port == 0 {
				if !live.Running() {
					return fmt.Errorf(
						"%s is %s, so KiwiVM is not reporting its SSH port\n\n"+
							"  Start it:      bwg power start --server %s\n"+
							"  Or pin a port: bwg server set %s --ssh-port <port>",
						s.Name, live.State(), s.Name, s.Name)
				}
				port = 22
			}

			login := user
			if login == "" {
				login = s.User()
			}

			sshArgs := []string{"-p", fmt.Sprint(port)}
			if identity != "" {
				sshArgs = append(sshArgs, "-i", identity)
			}
			if noHostCheck {
				sshArgs = append(sshArgs, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
			}
			sshArgs = append(sshArgs, login+"@"+host)
			sshArgs = append(sshArgs, args...)

			if printOnly || app.JSON || app.JQ != "" {
				payload := map[string]any{
					"server": s.Name, "host": host, "port": port, "user": login,
					"command": "ssh " + strings.Join(sshArgs, " "),
					"state":   live.State(),
				}
				return app.Emit(payload, func(w io.Writer) {
					fmt.Fprintf(w, "ssh %s\n", strings.Join(sshArgs, " "))
				})
			}

			bin, err := exec.LookPath("ssh")
			if err != nil {
				return fmt.Errorf("no ssh client on PATH: %w\n\n"+
					"  Print the command instead: bwg ssh --print", err)
			}
			app.Notef("%s ssh %s", output.Dim("→"), strings.Join(sshArgs, " "))

			child := exec.Command(bin, sshArgs...)
			child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := child.Run(); err != nil {
				var exitErr *exec.ExitError
				if ok := asExitError(err, &exitErr); ok {
					return &ExitCodeError{Code: exitErr.ExitCode(), Err: err}
				}
				return err
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&printOnly, "print", false, "Print the ssh command instead of running it")
	f.StringVarP(&user, "user", "u", "", "Login user (default: the server's ssh_user, or root)")
	f.BoolVar(&useIPv6, "ipv6", false, "Connect via IPv6 address instead of IPv4")
	f.BoolVar(&noHostCheck, "no-host-check", false, "Skip host key verification (useful after reinstall)")
	f.StringVarP(&identity, "identity", "i", "", "Path to SSH private key file")
	return cmd
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func newKeysCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Inspect and set the SSH keys a reinstall will install",
		Long: `KiwiVM stores SSH keys in two places:

  per-VM     Hypervisor Vault, set with 'bwg keys set'
  account    the billing portal, shared across every VPS

Per-VM keys shadow account keys entirely. Whichever set wins is what
reinstallOS writes to /root/.ssh/authorized_keys — these commands do
not touch a running guest.`,
	}
	cmd.AddCommand(keysLs(app), keysSet(app))
	return cmd
}

func keysLs(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Show the SSH keys stored for this VPS and account",
		Long: `List stored SSH keys.

JSON shape:
  {"server","perVM":[...],"account":[...],"preferred":[...],
   "effective":"per-VM"|"account"|"none"}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.Client()
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			keys, err := c.SSHKeys(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			perVM, account, preferred := keys.VeidSlice(), keys.UserSlice(), keys.PreferredSlice()

			effective := "none"
			switch {
			case len(perVM) > 0:
				effective = "per-VM"
			case len(account) > 0:
				effective = "account"
			}

			payload := map[string]any{
				"server": s.Name, "perVM": perVM, "account": account,
				"preferred": preferred, "effective": effective,
				"hints": map[string]string{"set": "bwg keys set ~/.ssh/id_ed25519.pub"},
			}
			return app.Emit(payload, func(w io.Writer) {
				output.Tabbed(w, [][2]string{
					{"Per-VM keys", fmt.Sprintf("%d %s", len(perVM), output.Dim("(Hypervisor Vault)"))},
					{"Account keys", fmt.Sprintf("%d %s", len(account), output.Dim("(billing portal)"))},
					{"Effective", effectiveNote(effective, len(preferred))},
				})
				if len(preferred) > 0 {
					fmt.Fprintf(w, "\n%s\n", output.Dim("Keys a reinstall would install:"))
					for _, k := range splitBrief(keys.ShortenedPreferred, preferred) {
						fmt.Fprintf(w, "  %s\n", k)
					}
				}
			})
		},
	}
}

func effectiveNote(effective string, n int) string {
	switch effective {
	case "per-VM":
		return fmt.Sprintf("%s %s", output.Good("per-VM"),
			output.Dim(fmt.Sprintf("(%d keys; account keys are shadowed)", n)))
	case "account":
		return fmt.Sprintf("%s %s", output.Good("account"), output.Dim(fmt.Sprintf("(%d keys)", n)))
	}
	return output.Warn("none — a reinstall would leave password-only access")
}

// splitBrief prefers KiwiVM's shortened rendering, falling back to the
// full keys when it is absent. Full keys are 400 characters and make
// the output unreadable.
func splitBrief(shortened string, full []string) []string {
	if s := strings.TrimSpace(shortened); s != "" {
		var out []string
		for _, line := range strings.Split(s, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	out := make([]string, 0, len(full))
	for _, k := range full {
		out = append(out, output.Truncate(k, 72))
	}
	return out
}

func keysSet(app *App) *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "set [key-file...]",
		Short: kiwivm.Ops["updateSshKeys"].Summary,
		Long: `Replace the per-VM SSH keys held in Hypervisor Vault.

Each argument is a path to a public key file; pass several to install
several. This REPLACES the existing per-VM keys rather than adding to
them. --clear removes them all, which restores the account-level keys.

These keys take effect on the next reinstall. They do not change a
running guest's authorized_keys.`,
		Example: `  bwg keys set ~/.ssh/id_ed25519.pub
  bwg keys set ~/.ssh/id_ed25519.pub ~/.ssh/backup.pub
  bwg keys set --clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !clear && len(args) == 0 {
				return fmt.Errorf("give at least one public key file, or --clear to remove them\n\n" +
					"  Example: bwg keys set ~/.ssh/id_ed25519.pub")
			}
			if clear && len(args) > 0 {
				return fmt.Errorf("--clear takes no key files")
			}

			var keys []string
			for _, path := range args {
				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("reading %s: %w", path, err)
				}
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					if !looksLikePublicKey(line) {
						return fmt.Errorf(
							"%s does not look like a public key\n\n"+
								"  Expected a line starting with ssh-rsa, ssh-ed25519 or ecdsa-....\n"+
								"  Did you pass the private key by mistake?", path)
					}
					keys = append(keys, line)
				}
			}

			c, s, err := app.ClientForOp(kiwivm.Ops["updateSshKeys"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts := [][2]string{{"Effect", "applies at the next reinstall, not to the running guest"}}
			if clear {
				facts = append(facts, [2]string{"Change", "remove all per-VM keys; account keys take over"})
			} else {
				for _, k := range keys {
					facts = append(facts, [2]string{"Key", output.Truncate(k, 60)})
				}
			}
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["updateSshKeys"], Server: s, Facts: facts,
			}); err != nil {
				return err
			}

			if err := c.UpdateSSHKeys(ctx, keys); err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "keys": len(keys), "cleared": clear,
					"hints": map[string]string{"verify": "bwg keys ls"}},
				func(w io.Writer) {
					if clear {
						fmt.Fprintf(w, "%s Per-VM keys cleared on %s; account keys apply now.\n",
							output.Good("✓"), output.Strong(s.Name))
						return
					}
					fmt.Fprintf(w, "%s %d key(s) stored for %s — they install on the next reinstall.\n",
						output.Good("✓"), len(keys), output.Strong(s.Name))
				})
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "Remove all per-VM keys")
	return cmd
}

func looksLikePublicKey(line string) bool {
	for _, prefix := range []string{"ssh-rsa", "ssh-ed25519", "ssh-dss", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func newPasswdCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "passwd",
		Short: kiwivm.Ops["resetRootPassword"].Summary,
		Long: `Generate a new root password for the VPS.

The current password stops working immediately, and anything using it
is locked out. The new one is shown once and cannot be retrieved
later, so capture it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, s, err := app.ClientForOp(kiwivm.Ops["resetRootPassword"])
			if err != nil {
				return err
			}
			ctx, cancel := app.Context(cmd.Context())
			defer cancel()

			facts, _ := app.identifyFacts(ctx, c)
			if err := app.Confirm(Consent{
				Op: kiwivm.Ops["resetRootPassword"], Server: s, Facts: facts,
			}); err != nil {
				return err
			}

			res, err := c.ResetRootPassword(ctx)
			if err != nil {
				return Explain(err, s.Name)
			}
			return app.Emit(
				map[string]any{"server": s.Name, "password": res.Password,
					"hints": map[string]string{"note": "shown once; KiwiVM cannot retrieve it again"}},
				func(w io.Writer) {
					fmt.Fprintf(w, "%s New root password for %s:\n\n  %s\n\n%s\n",
						output.Good("✓"), output.Strong(s.Name), output.Strong(res.Password),
						output.Warn("Save it now — KiwiVM will not show it again."))
				})
		},
	}
}
