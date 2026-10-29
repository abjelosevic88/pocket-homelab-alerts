// Package ai generates text through Pocket AI or a personal OpenAI-compatible
// endpoint. Direct OpenAI clients also provide models and audio transcription.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client holds the endpoint and key.
type Client struct {
	Pocket  *core.InferenceClient
	BaseURL string
	Key     string
	Model   string
	HTTP    *http.Client
}

// DefaultModel is used when none is configured.
const DefaultModel = "gpt-4o-mini"

// New creates a client.
func New(baseURL, key, model string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = DefaultModel
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: key, Model: model, HTTP: &http.Client{Timeout: timeout}}
}

// Configured reports whether a key is set.
func (c *Client) Configured() bool { return c != nil && (c.Pocket != nil || c.Key != "") }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Complete sends a system + user prompt and returns the assistant text.
// With jsonMode the model is asked for a single JSON object.
func (c *Client) Complete(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	if c.Pocket != nil {
		out, err := c.Pocket.Complete(ctx, system, user, jsonMode)
		return out.Text, err
	}

	if !c.Configured() {
		return "", errors.New("AI is not configured")
	}
	body := map[string]any{
		"model":    c.Model,
		"messages": []message{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return "", fmt.Errorf("AI HTTP %d: %s", res.StatusCode, e.Error.Message)
		}
		return "", fmt.Errorf("AI HTTP %d", res.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("AI returned no answer")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// JSON runs Complete in JSON mode and decodes into v.
func (c *Client) JSON(ctx context.Context, system, user string, v any) error {
	txt, err := c.Complete(ctx, system, user, true)
	if err != nil {
		return err
	}
	// Some models wrap JSON in a code fence despite json mode.
	txt = strings.TrimPrefix(strings.TrimPrefix(txt, "```json"), "```")
	txt = strings.TrimSuffix(txt, "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(txt)), v); err != nil {
		return fmt.Errorf("AI answer was not valid JSON: %w", err)
	}
	return nil
}

// Models lists available model ids (connection test).
func (c *Client) Models(ctx context.Context) ([]string, error) {
	if !c.Configured() {
		return nil, errors.New("AI is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("the API key was rejected (401)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AI HTTP %d", res.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}
