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
)

func main() {
	e := env{l: layout.Default(), euid: os.Geteuid(), out: os.Stdout}
	if err := run(os.Args[1:], e); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

// env is what run takes from the process, so tests can supply their own.
type env struct {
	l    layout.Layout
	euid int // 0 for root
	out  io.Writer
}

func (e env) root() bool { return e.euid == 0 }

func run(args []string, e env) error {
	if len(args) == 0 {
		io.WriteString(e.out, cli.Usage)
		return nil
	}
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
	case "provider":
		if len(args) != 4 || args[1] != "install" {
			return errors.New("usage: agent provider install NAME FILE")
		}
		if !e.root() {
			return errors.New("agent provider install needs root (sudo)")
		}
		return install(ctx, e.l, args[2], args[3])
	case "_fsio":
		return fsio.Serve(os.Stdin, os.Stdout)
	case "help", "-h", "--help":
		io.WriteString(e.out, cli.Usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], cli.Usage)
}

func runner(l layout.Layout) (*plugin.Runner, error) {
	cred, err := osuser.Lookup("agent-provider")
	if err != nil {
		return nil, err
	}
	return &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), User: cred, Timeout: 2 * time.Minute, Stderr: os.Stderr}, nil
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
		return cli.Describe(ctx, l, r, os.Stdout)
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
