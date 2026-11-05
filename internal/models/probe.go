package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/muse"
)

// Client builds the chat client for a backend.
func Client(b Backend) *muse.Client {
	return ClientWithKey(b, "")
}

// ClientWithKey builds a client using a process-only credential override.
// An explicitly configured environment variable takes precedence.
func ClientWithKey(b Backend, key string) *muse.Client {
	if b.KeyEnv != "" {
		if envKey := os.Getenv(b.KeyEnv); envKey != "" {
			key = envKey
		}
	}
	c := muse.New(key)
	c.BaseURL = b.BaseURL
	if b.Kind != "meta" {
		c.Effort = "" // reasoning_effort is a Meta API setting
		// a server that scales to zero may take minutes to load its weights
		c.HTTP = &http.Client{Timeout: 8 * time.Minute}
	}
	return c
}

// Probe asks the backend to prove what it can do: call a tool, and read a
// colour off an image. Speech is a Meta API product and
// keeps what the defaults say.
func Probe(ctx context.Context, b Backend) Caps {
	return ProbeWithKey(ctx, b, "")
}

// ProbeWithKey tests a backend without retaining its key in the registry.
func ProbeWithKey(ctx context.Context, b Backend, key string) Caps {
	caps := b.Caps
	caps.Checked = time.Now()
	if b.Kind == "meta" {
		return caps
	}
	c := ClientWithKey(b, key)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute) // the client waits out a model loading
	defer cancel()
	chat := func(msgs []core.Msg, tools []core.Tool) (core.Reply, error) { return c.Chat(ctx, b.Model, msgs, tools) }
	add := core.Tool{Name: "add", Description: "Add two numbers.", Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`)}
	r, err := chat([]core.Msg{core.Text("user", "Use the add tool to add 2 and 3.")}, []core.Tool{add})
	caps.Tools = err == nil && len(r.ToolCalls) > 0 && r.ToolCalls[0].Name == "add"
	notes := []string{}
	if err != nil {
		notes = append(notes, "tools: "+err.Error())
	}
	r, err = chat([]core.Msg{{Role: "user", Parts: []core.Part{
		{Type: "text", Text: "What colour fills this image? Answer with one word."},
		{Type: "image", ImageURL: redSquare()},
	}}}, nil)
	caps.Vision = err == nil && strings.Contains(strings.ToLower(r.Text), "red")
	if err != nil {
		notes = append(notes, "images: "+err.Error())
	}
	caps.Note = strings.Join(notes, "; ")
	return caps
}

func redSquare() string {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{220, 20, 20, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
