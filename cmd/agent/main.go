// Command agent is the harness: the daemon and its operator commands.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
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
	"github.com/SEAN-7/bot/internal/release"
	"github.com/SEAN-7/bot/internal/version"
)

func main() {
	e := env{l: layout.Default(), euid: os.Geteuid(), home: os.Getenv("HOME"), out: os.Stdout, wait: 10 * time.Second, poll: 200 * time.Millisecond}
	if err := run(os.Args[1:], e); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

// env is what run takes from the process, so tests can supply their own.
type env struct {
	l          layout.Layout
	euid       int // 0 for root
	home       string
	out        io.Writer
	wait, poll time.Duration // how long agent new waits for admission, and how often it looks
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
			err = cli.Messages(ctx, conversation, e.out)
		} else {
			err = cli.Log(ctx, journalDir, errorFile, e.out)
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "provider":
		return provider(ctx, l, args[1:])
	case "version":
		fmt.Fprintln(e.out, version.Version)
		return nil
	case "_fsio":
		return fsio.Serve(os.Stdin, os.Stdout)
	case "help", "-h", "--help":
		io.WriteString(e.out, cli.Usage+cli.SharingHelp)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], cli.Usage)
}

// messageText is text, or all of stdin for "-".
func messageText(text string) (string, error) {
	if text != "-" {
		return text, nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, cli.MessageLimit+1))
	return string(data), err
}

// options collects repeated --option flags.
type options []string

func (o *options) String() string     { return strings.Join(*o, ",") }
func (o *options) Set(v string) error { *o = append(*o, v); return nil }

func newAgent(e env, args []string) error {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // the error is returned once, by main
	var o cli.NewOptions
	var opts options
	flags.StringVar(&o.Provider, "provider", "", "provider (default: from your config, or the only one installed)")
	flags.StringVar(&o.Model, "model", "", "model (default: from your config; root must choose)")
	flags.Var(&opts, "option", "model option k=v (repeatable)")
	if err := flags.Parse(reorder(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(e.out, cli.NewUsage)
		}
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: agent new NAME [--provider P] [--model M] [--option k=v]...")
	}
	o.Options = opts
	o.Wait, o.Poll = e.wait, e.poll
	return cli.New(e.l, e.root(), e.home, flags.Arg(0), o, e.out)
}

func runner(l layout.Layout) (*plugin.Runner, error) {
	cred, err := osuser.Lookup("agent-provider")
	if err != nil {
		return nil, err
	}
	return &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), User: cred, Timeout: 2 * time.Minute, Stderr: os.Stderr}, nil
}

func provider(ctx context.Context, l layout.Layout, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agent provider install NAME|URL | agent provider describe")
	}
	root := os.Geteuid() == 0
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
		flags := flag.NewFlagSet("install", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		sha := flags.String("sha256", "", "expected SHA-256 when installing from a URL")
		const usage = "usage: agent provider install NAME|URL [--sha256 HEX]"
		if err := flags.Parse(reorder(args[1:])); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return errors.New(usage)
			}
			return err
		}
		if flags.NArg() != 1 {
			return errors.New(usage)
		}
		if !root {
			return errors.New("agent provider install needs root (sudo)")
		}
		return install(ctx, l, flags.Arg(0), *sha)
	}
	return fmt.Errorf("unknown provider command %q", args[0])
}

// valueFlags are the flags that take a value, as "--name value" or
// "--name=value".
var valueFlags = map[string]bool{"provider": true, "model": true, "option": true, "sha256": true}

// reorder moves flags before positional arguments, so flags may follow them.
// Only the flags in valueFlags take the next argument; any other flag stands
// alone, and flag reports it if it is unknown. After "--" everything is
// positional.
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append([]string{"--"}, args[i+1:]...)
			return append(flags, rest...)
		case len(arg) > 1 && arg[0] == '-':
			flags = append(flags, arg)
			if name := strings.TrimLeft(arg, "-"); !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		default:
			rest = append(rest, arg)
		}
	}
	return append(flags, rest...)
}

func install(ctx context.Context, l layout.Layout, target, sha string) error {
	settings, err := layout.LoadSettings(l.SettingsFile())
	if err != nil {
		return err
	}
	r, err := runner(l)
	if err != nil {
		return err
	}
	err = cli.Install(ctx, target, sha, cli.InstallOptions{
		Layout:        l,
		Release:       &release.Client{API: "https://api.github.com", Token: os.Getenv("GITHUB_TOKEN")},
		Repo:          settings.Repository,
		Version:       version.Version,
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
