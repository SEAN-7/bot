// Command agent is the harness: the daemon and its operator commands.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/cli"
	"github.com/SEAN-7/bot/internal/daemon"
	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/version"
)

func main() {
	e := env{l: layout.Default(), euid: os.Geteuid(), home: os.Getenv("HOME"), in: os.Stdin, out: os.Stdout, err: os.Stderr}
	if err := run(os.Args[1:], e); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

// env is what run takes from the process, so tests can supply their own.
type env struct {
	l    layout.Layout
	euid int // 0 for root
	home string
	in   io.Reader
	out  io.Writer
	err  io.Writer // notices, such as for a command that files replace
}

func (e env) root() bool { return e.euid == 0 }

func run(args []string, e env) error {
	if len(args) == 0 {
		io.WriteString(e.out, cli.Usage)
		return nil
	}
	l := e.l
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if notice := replaced(args); notice != "" {
		fmt.Fprintln(e.err, notice)
	}
	switch args[0] {
	case "daemon":
		if !e.root() {
			return errors.New("the daemon must run as root")
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		return daemon.Run(ctx, daemon.DefaultConfig(self))
	case "new":
		return newAgent(e, args[1:])
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: agent remove NAME")
		}
		if err := cli.Remove(l, e.root(), e.home, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Removed %s.\n", args[1])
		return nil
	case "send":
		root := e.root()
		if len(args) == 2 && root {
			return errors.New(`name an agent: sudo agent send NAME "text"`)
		}
		if len(args) != 3 {
			return errors.New(`usage: agent send NAME "text" (or - to read stdin)`)
		}
		text, err := messageText(args[2])
		if err != nil {
			return err
		}
		base, err := cli.Base(l, root, e.home)
		if err != nil {
			return err
		}
		if err := cli.SendChild(base, args[1], text, time.Now()); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Queued for %s.\n", args[1])
		return nil
	case "message":
		if len(args) != 2 {
			return errors.New(`usage: agent message "text" (or - to read stdin)`)
		}
		text, err := messageText(args[1])
		if err != nil {
			return err
		}
		if err := cli.Message(e.root(), e.home, text, time.Now()); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "Queued for your owner.")
		return nil
	case "messages", "log":
		if len(args) > 2 {
			return fmt.Errorf("usage: agent %s [NAME]", args[0])
		}
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		journalDir, errorFile, conversation, err := cli.Target(l, e.root(), e.home, name)
		if err != nil {
			return err
		}
		if args[0] == "messages" {
			return cli.Messages(conversation, e.out)
		}
		return cli.Log(journalDir, errorFile, e.out)
	case "provider":
		return provider(ctx, e, args[1:])
	case "version":
		fmt.Fprintln(e.out, version.Version)
		return nil
	case "_fsio":
		return fsio.Serve(os.Stdin, os.Stdout)
	case "help", "-h", "--help":
		io.WriteString(e.out, cli.Usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], cli.Usage)
}

// replacedBy names, for each command that files replace, the section of
// the manual that describes the files. The commands still work in 0.7.0 and
// are removed in 0.8.0.
var replacedBy = map[string]string{
	"new": "Subagents", "remove": "Subagents", "send": "Messages", "message": "Messages",
	"messages": "Messages", "log": "Journal", "version": "Runs, Upgrades",
}

// replaced is the notice for a command that files replace, or "".
func replaced(args []string) string {
	cmd, section := args[0], replacedBy[args[0]]
	if cmd == "provider" && len(args) > 1 && args[1] == "describe" {
		cmd, section = "provider describe", "Configuration"
	}
	if section == "" {
		return ""
	}
	return fmt.Sprintf("agent %s: replaced by files; see /var/lib/agent/manual/manual.md (%s). Removed in 0.8.0.", cmd, section)
}

// messageText is text, or all of stdin for "-".
func messageText(text string) (string, error) {
	if text != "-" {
		return text, nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, cli.MessageLimit+1))
	return string(data), err
}

// newAgent creates a subagent from a config file, stdin or the caller's own.
func newAgent(e env, args []string) error {
	if len(args) < 1 || len(args) > 2 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: agent new NAME [CONFIG|-]")
	}
	source := ""
	if len(args) == 2 {
		source = args[1]
	}
	return cli.New(e.l, e.root(), e.home, args[0], source, e.in, e.out)
}

func runner(l layout.Layout) (*plugin.Runner, error) {
	cred, err := osuser.Lookup("agent-provider")
	if err != nil {
		return nil, err
	}
	return &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), User: cred, Timeout: 2 * time.Minute, Stderr: os.Stderr}, nil
}

func provider(ctx context.Context, e env, args []string) error {
	l := e.l
	if len(args) == 0 {
		return errors.New("usage: agent provider install NAME FILE | agent provider describe")
	}
	root := e.root()
	switch args[0] {
	case "describe":
		var r *plugin.Runner
		if root {
			var err error
			if r, err = runner(l); err != nil {
				return err
			}
		}
		return cli.Describe(ctx, l, r, root, os.Stdout)
	case "install":
		if len(args) != 3 {
			return errors.New("usage: agent provider install NAME FILE")
		}
		if !root {
			return errors.New("agent provider install needs root (sudo)")
		}
		return install(ctx, l, args[1], args[2])
	}
	return fmt.Errorf("unknown provider command %q", args[0])
}

// install installs the plugin binary install.sh downloaded to file as name.
func install(ctx context.Context, l layout.Layout, name, file string) error {
	r, err := runner(l)
	if err != nil {
		return err
	}
	err = cli.Install(ctx, name, file, cli.InstallOptions{
		Layout:        l,
		Arch:          runtime.GOARCH,
		ProviderGroup: "agent-provider",
		Prompt:        prompt,
		Runner:        r,
	})
	if err == nil {
		fmt.Println("Installed. Models:")
		return cli.Describe(ctx, l, r, true, os.Stdout)
	}
	return err
}

// prompt asks on the terminal, even when stdin is a pipe (curl | sh).
// Secrets are read with echo turned off.
func prompt(s proto.Setting) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("%s is not set and there is no terminal to ask on", s.Name)
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s (%s): ", s.Name, s.Description)
	if s.Secret {
		stty := func(arg string) {
			cmd := exec.Command("stty", arg)
			cmd.Stdin = tty
			cmd.Run()
		}
		stty("-echo")
		defer func() { stty("echo"); fmt.Fprintln(tty) }()
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
