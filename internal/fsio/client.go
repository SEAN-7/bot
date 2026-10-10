package fsio

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Client sends requests to the helper. It is safe for concurrent use and
// restarts the helper if it dies.
type Client struct {
	self string
	user *syscall.Credential

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	dec   *json.Decoder
	dead  bool
}

// Start launches `self _fsio` with the given credential (nil: current user).
func Start(self string, user *syscall.Credential) (*Client, error) {
	c := &Client{self: self, user: user}
	if err := c.spawn(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) spawn() error {
	cmd := exec.Command(c.self, "_fsio")
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: c.user}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd, c.stdin, c.dec, c.dead = cmd, stdin, json.NewDecoder(bufio.NewReader(stdout)), false
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stdin.Close()
	return c.cmd.Wait()
}

func (c *Client) call(req request) (response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		c.stdin.Close()
		c.cmd.Wait()
		if err := c.spawn(); err != nil {
			return response{}, fmt.Errorf("restart file helper: %w", err)
		}
	}
	var resp response
	err := json.NewEncoder(c.stdin).Encode(req)
	if err == nil {
		err = c.dec.Decode(&resp)
	}
	if err != nil {
		c.dead = true
		return response{}, fmt.Errorf("file helper: %w", err)
	}
	if resp.Err != "" {
		switch {
		case resp.NotExist:
			return resp, fmt.Errorf("%s: %w", resp.Err, fs.ErrNotExist)
		case resp.Permission:
			return resp, fmt.Errorf("%s: %w", resp.Err, fs.ErrPermission)
		}
		return resp, fmt.Errorf("%s", resp.Err)
	}
	return resp, nil
}

func (c *Client) Scan(home string) ([]string, error) {
	resp, err := c.call(request{Op: "scan", Path: home})
	return resp.Paths, err
}

func (c *Client) List(dir string) ([]Entry, error) {
	resp, err := c.call(request{Op: "list", Path: dir})
	return resp.Entries, err
}

// Read returns at most limit bytes of a regular file and its full size.
func (c *Client) Read(path string, limit int64) ([]byte, int64, error) {
	resp, err := c.call(request{Op: "read", Path: path, Limit: limit})
	return resp.Data, resp.Size, err
}

func (c *Client) Write(path string, data []byte) error {
	_, err := c.call(request{Op: "write", Path: path, Data: data})
	return err
}

func (c *Client) Rename(from, to string) error {
	_, err := c.call(request{Op: "rename", Path: from, Path2: to})
	return err
}

func (c *Client) Mkdir(path string) error {
	_, err := c.call(request{Op: "mkdir", Path: path})
	return err
}

func (c *Client) Remove(path string) error {
	_, err := c.call(request{Op: "remove", Path: path})
	return err
}

// Symlink points path at target, replacing whatever was at path.
func (c *Client) Symlink(target, path string) error {
	_, err := c.call(request{Op: "symlink", Path: target, Path2: path})
	return err
}

func (c *Client) Stat(path string) (Entry, error) {
	resp, err := c.call(request{Op: "stat", Path: path})
	if err != nil {
		return Entry{}, err
	}
	return *resp.Entry, nil
}
