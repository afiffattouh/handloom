// Package client is the HTTP client used by the CLI and the link, plus the
// link's on-disk configuration.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"handloom/internal/setting"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"handloom/internal/api"
)

type Client struct {
	HTTP     *http.Client
	Base     string // hub URL, or http://link for the local socket
	Token    string // bearer token; empty when talking to the link
	Agent    string // sent as Handloom-Agent
	RunToken string // the agent's run token, if it has one; sent as Handloom-Run-Token
}

// Error is an error answer from the hub.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Direct talks straight to the hub with a token (admin, human or device).
func Direct(hubURL, token string) *Client {
	return &Client{HTTP: &http.Client{}, Base: strings.TrimRight(hubURL, "/"), Token: token}
}

// Socket talks to the local link, which adds the device credential.
func Socket(path, agent string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	c := &Client{HTTP: &http.Client{Transport: tr}, Base: "http://link", Agent: agent}
	if agent != "" {
		c.RunToken = LoadRunToken(agent)
	}
	return c
}

func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Agent != "" {
		req.Header.Set(api.AgentHeader, c.Agent)
	}
	if c.RunToken != "" {
		req.Header.Set(api.RunTokenHeader, c.RunToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		var ae api.Error
		if json.Unmarshal(raw, &ae) == nil && ae.Error != "" {
			return &Error{Status: resp.StatusCode, Code: ae.Code, Msg: ae.Error}
		}
		return &Error{Status: resp.StatusCode, Code: "http", Msg: fmt.Sprintf("%s %s: %s", method, path, resp.Status)}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) Get(path string, out any) error {
	return c.Do(context.Background(), "GET", path, nil, out)
}

func (c *Client) Post(path string, body, out any) error {
	if body == nil {
		body = struct{}{}
	}
	return c.Do(context.Background(), "POST", path, body, out)
}

// ---- link configuration ----

// Home is where the link keeps its credential and socket: $HANDLOOM_HOME, or
// ~/.config/handloom.
func Home() string {
	if h := setting.Get("HOME"); h != "" {
		return h
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, ".config", "handloom")
}

func SocketPath() string { return filepath.Join(Home(), "link.sock") }

type LinkConfig struct {
	Hub        string `json:"hub"`
	Device     string `json:"device"`
	Credential string `json:"credential"`
}

func configPath() string { return filepath.Join(Home(), "link.json") }

func LoadLinkConfig() (*LinkConfig, error) {
	b, err := os.ReadFile(configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("this device has not joined a hub: run `handloom link join <hub-url> <join-token>`")
		}
		return nil, err
	}
	var cfg LinkConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath(), err)
	}
	return &cfg, nil
}

// SaveLinkConfig writes the device credential, readable by the owner only.
func SaveLinkConfig(cfg *LinkConfig) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// ---- link state (event cursor) ----

func cursorPath() string { return filepath.Join(Home(), "cursor") }

func LoadCursor() int64 {
	b, err := os.ReadFile(cursorPath())
	if err != nil {
		return 0
	}
	var n int64
	fmt.Sscan(string(b), &n)
	return n
}

func SaveCursor(n int64) {
	os.WriteFile(cursorPath(), []byte(fmt.Sprintf("%d\n", n)), 0o600)
}

// Wait is a small helper for retry loops.
func Wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---- run tokens ----

// A run token lives in <project>/.handloom/tokens/<agent> (mode 0600), next
// to the .handloom/agent file that names the agent. Every process the agent
// runs finds it by walking up from its working directory; the hub checks that
// it belongs to the agent that is speaking.

func tokenFile(dir, agent string) string {
	return filepath.Join(dir, ".handloom", "tokens", agent)
}

// LoadRunToken returns the token for agent from the nearest project directory
// that has one, or "".
func LoadRunToken(agent string) string {
	if agent == "" || strings.ContainsAny(agent, `/\`) {
		return ""
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir != "" {
		if b, err := os.ReadFile(tokenFile(dir, agent)); err == nil {
			return strings.TrimSpace(string(b))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// SaveRunToken writes the token for agent into dir.
func SaveRunToken(dir, agent, token string) error {
	if strings.ContainsAny(agent, `/\`) {
		return fmt.Errorf("bad agent name %q", agent)
	}
	path := tokenFile(dir, agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token+"\n"), 0o600)
}

// RemoveRunToken deletes the token file for agent in dir.
func RemoveRunToken(dir, agent string) {
	os.Remove(tokenFile(dir, agent))
	os.Remove(filepath.Dir(tokenFile(dir, agent))) // only when empty
}
