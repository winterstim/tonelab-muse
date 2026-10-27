package core

import (
	"encoding/json"
)

// Msg is one chat message in the OpenAI chat format Muse speaks.
type Msg struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Parts      []Part     `json:"parts,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Part is one piece of message content.
type Part struct {
	Type     string `json:"type"` // text, image, audio
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"` // data: URL or https
	AudioB64 string `json:"audio_b64,omitempty"`
	Format   string `json:"format,omitempty"` // wav, mp3
}

// Text is a message with a single text part.
func Text(role, s string) Msg { return Msg{Role: role, Parts: []Part{{Type: "text", Text: s}}} }

// Tool is a function the model may call. Schema is a JSON Schema object.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

type Usage struct {
	In        int `json:"prompt_tokens"`
	Out       int `json:"completion_tokens"`
	Cached    int `json:"cached_tokens"`
	Reasoning int `json:"reasoning_tokens"`
}

// Reply is the model's answer: text, tool calls, or both.
type Reply struct {
	Text      string     `json:"text"`
	ToolCalls []ToolCall `json:"tool_calls"`
	Usage     Usage      `json:"usage"`
}
