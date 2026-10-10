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
	"github.com/SEAN-7/bot/internal/seed"
	"github.com/SEAN-7/bot/internal/version"
)

const usage = `Usage:
  agent daemon                      run the service (root)
  agent send "text"                 send an owner message to the main agent (root)
  agent log [NAME]                  follow an agent's journal (NAME under ~/agents)
  agent provider install NAME|URL   install a provider plugin (root; URL needs --sha256)
  agent provider describe           show providers, models and options
  agent version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	l := layout.Default()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "daemon":
		if os.Geteuid() != 0 {
			return errors.New("the daemon must run as root")
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		return daemon.Run(ctx, daemon.DefaultConfig(self))
	case "send":
		if len(args) != 2 {
			return errors.New(`usage: agent send "text"`)
		}
		if err := cli.Send(l, args[1], time.Now()); err != nil {
			return err
		}
		fmt.Println("Sent.")
		return nil
	case "log":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		err := cli.Log(ctx, l, name, os.Stdout)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case "provider":
		return provider(ctx, l, args[1:])
	case "version":
		fmt.Println(version.Version)
		return nil
	case "_fsio":
		return fsio.Serve(os.Stdin, os.Stdout)
	case "_seed":
		if len(args) != 2 {
			return errors.New("usage: agent _seed DIR < config.json")
		}
		config, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		return seed.Write(args[1], config)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
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
		sha := flags.String("sha256", "", "expected SHA-256 when installing from a URL")
		if err := flags.Parse(reorder(args[1:])); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: agent provider install NAME|URL [--sha256 HEX]")
		}
		if !root {
			return errors.New("agent provider install needs root (sudo)")
		}
		return install(ctx, l, flags.Arg(0), *sha)
	}
	return fmt.Errorf("unknown provider command %q", args[0])
}

// reorder moves flags before positional arguments, so flags may follow them.
func reorder(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			rest = append(rest, args[i])
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
