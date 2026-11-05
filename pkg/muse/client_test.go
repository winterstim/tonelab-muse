package muse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func server(t *testing.T, h func(req map[string]any) (int, string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/chat/completions" {
			t.Errorf("auth %q path %q", r.Header.Get("Authorization"), r.URL.Path)
		}
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		code, body := h(req)
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New("k")
	c.BaseURL = srv.URL
	return c
}

const toolReply = `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"describe_chain","arguments":"{\"track\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":15}}}`

func TestChatToolsAndParts(t *testing.T) {
	var seen map[string]any
	c := server(t, func(req map[string]any) (int, string) { seen = req; return 200, toolReply })
	var usage core.Usage
	c.OnUsage = func(_ string, u core.Usage) { usage = u }
	msgs := []core.Msg{
		core.Text("system", "you control a DAW"),
		{Role: "user", Parts: []core.Part{{Type: "text", Text: "look"}, {Type: "image", ImageURL: "data:image/png;base64,AA"}}},
	}
	tools := []core.Tool{{Name: "describe_chain", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}}
	r, err := c.Chat(context.Background(), "muse-spark-1.3", msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "describe_chain" || string(r.ToolCalls[0].Args) != `{"track":1}` {
		t.Fatalf("reply = %+v", r)
	}
	if usage.In != 100 || usage.Reasoning != 15 || c.Used() != 120 {
		t.Fatalf("usage = %+v used %d", usage, c.Used())
	}
	if seen["model"] != "muse-spark-1.3" || seen["reasoning_effort"] != "medium" {
		t.Fatalf("request = %v", seen)
	}
	ms := seen["messages"].([]any)
	if ms[0].(map[string]any)["content"] != "you control a DAW" {
		t.Fatalf("system content = %v", ms[0])
	}
	parts := ms[1].(map[string]any)["content"].([]any)
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("image part = %v", parts[1])
	}
	if seen["tools"].([]any)[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools = %v", seen["tools"])
	}
}

func TestBudget(t *testing.T) {
	c := server(t, func(map[string]any) (int, string) { return 200, toolReply })
	c.Budget = 100
	if _, err := c.Chat(context.Background(), "m", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(context.Background(), "m", nil, nil); !errors.Is(err, core.ErrBudget) {
		t.Fatalf("want ErrBudget, got %v", err)
	}
}

func TestRetryOnce5xx(t *testing.T) {
	n := 0
	c := server(t, func(map[string]any) (int, string) {
		n++
		if n == 1 {
			return 503, "busy"
		}
		return 200, toolReply
	})
	if _, err := c.Chat(context.Background(), "m", nil, nil); err != nil || n != 2 {
		t.Fatalf("err %v after %d calls", err, n)
	}
}

func TestNoRetry4xx(t *testing.T) {
	n := 0
	c := server(t, func(map[string]any) (int, string) { n++; return 401, `{"error":"invalid_api_key"}` })
	_, err := c.Chat(context.Background(), "m", nil, nil)
	if err == nil || n != 1 || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v after %d calls", err, n)
	}
}

func TestRuntimeKeyHeaderAndErrorRedaction(t *testing.T) {
	const fakeKey = "fake-key-never-persist-this"
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid key `+fakeKey+`"}`)
	}))
	defer srv.Close()

	c := New("")
	c.BaseURL = srv.URL
	c.SetKey(fakeKey)
	if !c.KeySet() {
		t.Fatal("key should be set")
	}
	_, err := c.Chat(context.Background(), "m", nil, nil)
	if seen != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", seen)
	}
	if err == nil || strings.Contains(err.Error(), fakeKey) {
		t.Fatalf("error exposed key: %v", err)
	}
}

func TestSuccessfulModelResponseDoesNotReturnKey(t *testing.T) {
	const fakeKey = "fake-key-never-return-this"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+fakeKey+`"}}],"usage":{}}`)
	}))
	defer srv.Close()
	c := New("")
	c.BaseURL = srv.URL
	c.SetKey(fakeKey)
	reply, err := c.Chat(context.Background(), "m", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply.Text, fakeKey) {
		t.Fatal("model response exposed the API key")
	}
}

func TestResponsesUsesRuntimeKey(t *testing.T) {
	const fakeKey = "fake-key-for-vision"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"api_key":"`+fakeKey+`"}`)
	}))
	defer srv.Close()
	c := New("")
	c.BaseURL = srv.URL
	c.SetKey(fakeKey)
	response, err := c.responses(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(response), fakeKey) {
		t.Fatal("response API exposed the API key")
	}
	if gotAuth != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
}

func TestReasoningAteBudget(t *testing.T) {
	c := server(t, func(map[string]any) (int, string) {
		return 200, `{"choices":[{"message":{"content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":16000}}`
	})
	if _, err := c.Chat(context.Background(), "m", nil, nil); err == nil || !strings.Contains(err.Error(), "output budget") {
		t.Fatalf("err = %v", err)
	}
}

func TestTranscribe(t *testing.T) {
	const fakeKey = "fake-voice-key-never-return-this"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/asr/transcribe" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeKey {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.FormValue("request"), `"PUSH_TO_TALK"`) {
			t.Errorf("request part %q", r.FormValue("request"))
		}
		f, _, err := r.FormFile("audio")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		if string(b) != "RIFF" {
			t.Errorf("audio %q", b)
		}
		_, _ = io.WriteString(w, `{"sessionId":"s","transcript":" less `+fakeKey+` ","audioDurationMs":900,"turns":[]}`)
	}))
	defer srv.Close()
	c := New("")
	c.BaseURL = srv.URL
	c.SetKey(fakeKey)
	got, err := Voice{Client: c, Model: "voice-1", Words: func() []string { return []string{"EQ Eight"} }}.Transcribe(context.Background(), []byte("RIFF"))
	if err != nil || got != "less [redacted]" || strings.Contains(got, fakeKey) {
		t.Fatalf("got %q, %v", got, err)
	}
}
