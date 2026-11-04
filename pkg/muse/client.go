// Package muse is a client for Meta's Model API (Chat Completions) that
// implements core.LLM for every Muse model: text, images, audio, tools.
// It counts tokens per session and refuses calls past a budget.
package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

const DefaultBaseURL = "https://api.meta.ai/v1"

// Client calls a chat-completions-compatible endpoint.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	OnWait  func() // optional: the model is still loading (called once per wait)
	// Effort is sent as reasoning_effort ("" = model default). Muse Spark
	// reasons inside the output budget, so MaxTokens must leave room.
	Effort    string
	MaxTokens int
	// Budget caps prompt+completion tokens across all calls; 0 = no cap.
	Budget int
	// OnUsage, when set, sees every call's usage (for logging).
	OnUsage func(model string, u core.Usage)

	mu   sync.Mutex
	used int
}

func New(key string) *Client {
	return &Client{
		BaseURL:   DefaultBaseURL,
		Key:       key,
		HTTP:      &http.Client{Timeout: 3 * time.Minute},
		Effort:    "medium",
		MaxTokens: 16000,
	}
}

// Used reports tokens spent so far.
func (c *Client) Used() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func (c *Client) Chat(ctx context.Context, model string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
	if c.Budget > 0 && c.Used() >= c.Budget {
		return core.Reply{}, core.ErrBudget
	}
	body := request{Model: model, MaxTokens: c.MaxTokens, ReasoningEffort: c.Effort, Temperature: 0}
	for _, m := range msgs {
		body.Messages = append(body.Messages, toWire(m))
	}
	for _, t := range tools {
		body.Tools = append(body.Tools, wireTool{Type: "function", Function: wireFunc{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
	}
	b, err := json.Marshal(body)
	if err != nil {
		return core.Reply{}, err
	}
	var res response
	waited := false
	for attempt := 0; ; attempt++ {
		res, err = c.post(ctx, b)
		var se statusError
		if errors.As(err, &se) && se.code == 503 && strings.Contains(se.Error(), "Loading model") {
			// a server waking from scale-to-zero: wait, it is not failing
			if c.OnWait != nil && !waited {
				c.OnWait()
			}
			waited = true
			select {
			case <-ctx.Done():
				return core.Reply{}, err
			case <-time.After(5 * time.Second):
			}
			attempt = -1
			continue
		}
		if err == nil || attempt == 1 || !(errors.As(err, &se) && se.code >= 500 || errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
			break
		}
	}
	if err != nil {
		return core.Reply{}, err
	}
	u := core.Usage{In: res.Usage.Prompt, Out: res.Usage.Completion, Cached: res.Usage.PromptDetails.Cached, Reasoning: res.Usage.CompletionDetails.Reasoning}
	c.mu.Lock()
	c.used += u.In + u.Out
	c.mu.Unlock()
	if c.OnUsage != nil {
		c.OnUsage(model, u)
	}
	if len(res.Choices) == 0 {
		return core.Reply{Usage: u}, errors.New("muse: empty response")
	}
	ch := res.Choices[0]
	r := core.Reply{Usage: u}
	if ch.Message.Content != nil {
		r.Text = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, core.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: json.RawMessage(tc.Function.Arguments)})
	}
	if r.Text == "" && len(r.ToolCalls) == 0 && ch.FinishReason == "length" {
		return r, fmt.Errorf("muse: reasoning used the whole output budget (%d tokens); raise MaxTokens or lower Effort", c.MaxTokens)
	}
	return r, nil
}

type statusError struct {
	code int
	body string
}

func (e statusError) Error() string { return fmt.Sprintf("muse: HTTP %d: %s", e.code, e.body) }

func (c *Client) post(ctx context.Context, body []byte) (response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return response{}, statusError{resp.StatusCode, msg}
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return response{}, fmt.Errorf("muse: bad JSON: %w", err)
	}
	return r, nil
}

// wire format

type request struct {
	Model           string        `json:"model"`
	Messages        []wireMessage `json:"messages"`
	Tools           []wireTool    `json:"tools,omitempty"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	Temperature     float64       `json:"temperature"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string   `json:"type"`
	Function wireFunc `json:"function"`
}

type wireFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content   *string        `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		Prompt        int `json:"prompt_tokens"`
		Completion    int `json:"completion_tokens"`
		PromptDetails struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func toWire(m core.Msg) wireMessage {
	w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		var x wireToolCall
		x.ID, x.Type = tc.ID, "function"
		x.Function.Name, x.Function.Arguments = tc.Name, string(tc.Args)
		w.ToolCalls = append(w.ToolCalls, x)
	}
	// plain text stays a string: tool and assistant messages expect that
	if len(m.Parts) == 1 && m.Parts[0].Type == "text" {
		w.Content = m.Parts[0].Text
		return w
	}
	if len(m.Parts) == 0 {
		w.Content = ""
		return w
	}
	var parts []map[string]any
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case "image":
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": p.ImageURL}})
		case "audio":
			parts = append(parts, map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": p.AudioB64, "format": p.Format}})
		}
	}
	w.Content = parts
	return w
}
