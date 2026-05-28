// Package sdk provides a Go client for interacting with a flagbase server.
// Embed this in user-defined functions or services that need context-aware
// feature evaluation without re-implementing the HTTP protocol.
package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a flagbase HTTP client bound to a single server endpoint.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a Client pointed at baseURL.
// Pass a Bearer token obtained from POST /auth/login to authenticate evaluations.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// EvaluateFlag asks the server to evaluate flag key in the context of the
// authenticated user (identified by the token). Returns false on any error.
func (c *Client) EvaluateFlag(key string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/flags/%s/evaluate", c.baseURL, key), nil)
	if err != nil {
		return false, err
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("evaluate flag: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Value bool `json:"value"`
	}
	return result.Value, json.NewDecoder(resp.Body).Decode(&result)
}

// RecordEvent publishes a metric event for a flag variant to the server.
func (c *Client) RecordEvent(flagKey, variant, eventType string, value float64) error {
	payload, err := json.Marshal(map[string]interface{}{
		"flag_key":   flagKey,
		"variant":    variant,
		"event_type": eventType,
		"value":      value,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/metrics", c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("record event: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
