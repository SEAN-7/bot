// Package plugin runs provider plugins and validates agent configuration.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/proto"
)

// validName keeps an agent-controlled provider name inside the plugin directory.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Runner executes plugins as the provider user, passing credentials in the
// environment. Neither the plugin nor the agent reads the credentials files.
type Runner struct {
	PluginDir string
	CredDir   string
	User      *syscall.Credential // nil: current user
	Timeout   time.Duration
	Stderr    io.Writer
}

func (r *Runner) Installed() ([]string, error) {
	entries, err := os.ReadDir(r.PluginDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && validName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (r *Runner) Describe(ctx context.Context, provider string) (proto.DescribeResponse, error) {
	var resp proto.DescribeResponse
	err := r.run(ctx, provider, map[string]any{"protocol": proto.Version, "type": "describe"}, &resp)
	if err == nil && resp.Error != nil {
		err = errors.New(resp.Error.Message)
	}
	return resp, err
}

func (r *Runner) Complete(ctx context.Context, provider string, req proto.CompleteRequest) (proto.CompleteResponse, error) {
	req.Protocol, req.Type = proto.Version, "complete"
	var resp proto.CompleteResponse
	err := r.run(ctx, provider, req, &resp)
	if err == nil && resp.Error != nil {
		err = &ProviderError{Message: resp.Error.Message, Kind: resp.Error.Kind}
	}
	return resp, err
}

// ProviderError is an error reported by the plugin, as opposed to a failure
// to run it.
type ProviderError struct {
	Message string
	Kind    string
}

func (e *ProviderError) Error() string { return e.Message }

// IsContextOverflow reports whether err says the request was too large.
func IsContextOverflow(err error) bool {
	var p *ProviderError
	return errors.As(err, &p) && p.Kind == proto.ErrContextOverflow
}

func (r *Runner) run(ctx context.Context, provider string, req, resp any) error {
	if !validName.MatchString(provider) {
		return fmt.Errorf("invalid provider name %q", provider)
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=/"}
	data, err := os.ReadFile(filepath.Join(r.CredDir, provider+".env"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	creds, err := ParseEnv(data)
	if err != nil {
		return fmt.Errorf("credentials for %s: %w", provider, err)
	}
	for k, v := range creds {
		env = append(env, k+"="+v)
	}
	input, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, filepath.Join(r.PluginDir, provider))
	cmd.Dir = "/"
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = r.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: r.User}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("plugin %s: %w", provider, err)
	}
	if err := json.Unmarshal(stdout.Bytes(), resp); err != nil {
		return fmt.Errorf("plugin %s: invalid response: %w", provider, err)
	}
	return nil
}
