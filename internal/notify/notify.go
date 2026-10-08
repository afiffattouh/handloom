// Package notify tells the human that something needs them. A notification
// carries a short generic line and a link, never the content of a question
// or an answer: phone push services are third parties.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Notification kinds.
const (
	KindEscalation = "escalation" // the lead asked the human a question
	KindLeadLost   = "lead-lost"  // a lead went offline with work unfinished
)

type Notification struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Link  string `json:"link,omitempty"`
}

type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Multi sends to every notifier and returns the first error after trying all.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, n Notification) error {
	var first error
	for _, x := range m {
		if err := x.Notify(ctx, n); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var topicRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Ntfy publishes to an ntfy server (https://ntfy.sh or self-hosted).
type Ntfy struct {
	URL   string // server, for example https://ntfy.sh
	Topic string
	Token string // optional access token for protected topics
	HTTP  *http.Client
}

// NewNtfy validates the settings.
func NewNtfy(serverURL, topic, token string) (*Ntfy, error) {
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("bad ntfy URL %q: use http:// or https://", serverURL)
	}
	if !topicRE.MatchString(topic) {
		return nil, fmt.Errorf("bad ntfy topic %q: use letters, digits, '_' or '-'", topic)
	}
	return &Ntfy{URL: strings.TrimRight(serverURL, "/"), Topic: topic, Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (n *Ntfy) Notify(ctx context.Context, nf Notification) error {
	req, err := http.NewRequestWithContext(ctx, "POST", n.URL+"/"+n.Topic, strings.NewReader(nf.Text))
	if err != nil {
		return err
	}
	req.Header.Set("Title", nf.Title)
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "speech_balloon")
	if nf.Link != "" {
		req.Header.Set("Click", nf.Link)
		// the hub's own icon, from the same address the link points at
		if u, err := url.Parse(nf.Link); err == nil && u.Scheme != "" && u.Host != "" {
			req.Header.Set("Icon", u.Scheme+"://"+u.Host+"/static/apple-touch-icon.png")
		}
	}
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	return send(n.HTTP, req)
}

// Webhook posts the notification as JSON.
type Webhook struct {
	URL  string
	HTTP *http.Client
}

func NewWebhook(rawURL string) (*Webhook, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("bad webhook URL %q: use http:// or https://", rawURL)
	}
	return &Webhook{URL: rawURL, HTTP: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (w *Webhook) Notify(ctx context.Context, nf Notification) error {
	b, _ := json.Marshal(nf)
	req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return send(w.HTTP, req)
}

func send(c *http.Client, req *http.Request) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notification endpoint answered %s", resp.Status)
	}
	return nil
}
