package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGitHub(t *testing.T, assetName string, body []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases/tags/v1.0.0":
			fmt.Fprintf(w, `{"assets":[{"id":3,"name":"other"},{"id":7,"name":%q}]}`, assetName)
		case "/repos/o/r/releases/assets/7":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "wrong accept", 400)
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAssetDownload(t *testing.T) {
	srv := fakeGitHub(t, "plugin-x-linux-amd64", []byte("binary"))
	defer srv.Close()
	c := Client{API: srv.URL, Token: "tok"}
	data, err := c.Asset(context.Background(), "o/r", "v1.0.0", "plugin-x-linux-amd64")
	if err != nil || string(data) != "binary" {
		t.Fatalf("got %q %v", data, err)
	}
	if _, err := c.Asset(context.Background(), "o/r", "v1.0.0", "missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing asset: %v", err)
	}
	if _, err := (&Client{API: srv.URL, Token: "bad"}).Asset(context.Background(), "o/r", "v1.0.0", "plugin-x-linux-amd64"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestAssetNeedsToken(t *testing.T) {
	_, err := (&Client{API: "http://unused"}).Asset(context.Background(), "o/r", "v1", "x")
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("got %v", err)
	}
}
