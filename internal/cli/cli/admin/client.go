package admin

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client speaks HTTPS to a leyline-server's /_leyline/operator/* and /_leyline/admin/{vault}/* routes.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// NewClient builds a Client bound to serverURL (https://host[:port]) using
// key as Bearer. InsecureSkipVerify is for testing only — never enable
// in production deploys.
func NewClient(serverURL, key string, insecureSkipVerify bool) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify}, //nolint:gosec — controlled by --insecure flag
	}
	return &Client{
		BaseURL: serverURL,
		Key:     key,
		HTTP:    &http.Client{Timeout: 60 * time.Second, Transport: tr},
	}
}

// Do issues an HTTPS request and decodes the JSON response into out (when non-nil).
// 4xx/5xx responses are surfaced as errors that include the server's "error" field
// when present, otherwise the raw body.
func (c *Client) Do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(respBody, &e)
		if e.Error != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
