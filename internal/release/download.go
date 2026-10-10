// Package release downloads assets from this project's private GitHub releases.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	API   string // https://api.github.com
	Token string
}

func (c *Client) get(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

// Asset downloads a release asset by name. The private repository needs a
// token; it is used for this request only and never stored.
func (c *Client) Asset(ctx context.Context, repo, tag, name string) ([]byte, error) {
	if c.Token == "" {
		return nil, errors.New("GITHUB_TOKEN is required to download from the private repository")
	}
	body, err := c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.API, repo, tag), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var rel struct {
		Assets []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	for _, a := range rel.Assets {
		if a.Name == name {
			return c.get(ctx, fmt.Sprintf("%s/repos/%s/releases/assets/%d", c.API, repo, a.ID), "application/octet-stream")
		}
	}
	return nil, fmt.Errorf("release %s has no asset %s", tag, name)
}
