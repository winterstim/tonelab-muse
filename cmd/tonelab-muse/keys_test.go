package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/models"
	"github.com/winterstim/tonelab-muse/internal/server"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/muse"
)

func TestModelKeyIsUsedImmediatelyAndNeverPersisted(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	const fakeKey = "fake-key-never-persist-this"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	meta := muse.New("")
	keys := newRuntimeKeys(meta)
	path := filepath.Join(t.TempDir(), "models.json")
	registry := models.Registry{Backends: []models.Backend{{Name: "Private endpoint", Kind: "compatible", BaseURL: srv.URL, Model: "test-model"}}}
	settings := &settings{path: path, reg: models.Registry{}, keys: keys, apply: func(context.Context, models.Registry) error { return nil }}
	if err := settings.Apply(context.Background(), registry, "Private endpoint", fakeKey); err != nil {
		t.Fatal(err)
	}

	client := models.ClientWithKey(registry.Backends[0], keys.ModelKey("Private endpoint"))
	if _, err := client.Chat(context.Background(), "test-model", []core.Msg{core.Text("user", "hello")}, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), fakeKey) {
		t.Fatal("model key was written to models.json")
	}
	if strings.Contains(string(data), "api_key") {
		t.Fatal("models.json contains an API key field")
	}
	if meta.KeySet() {
		t.Fatal("a custom model key was applied to Meta Model API calls")
	}
}

func TestEnvironmentModelKeyWins(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "environment-key")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()
	meta := muse.New(os.Getenv("MODEL_API_KEY"))
	meta.BaseURL = srv.URL
	keys := newRuntimeKeys(meta)
	keys.SetModelAPIKey("ui-key")
	if !keys.ModelAPIKeySet() || !meta.KeySet() {
		t.Fatal("environment key should remain configured")
	}
	if _, err := meta.Chat(context.Background(), "model", []core.Msg{core.Text("user", "hello")}, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer environment-key" {
		t.Fatal("UI key replaced MODEL_API_KEY")
	}
}

func TestSettingsModelAPIKeyAppliesToSharedMetaClientImmediately(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	const fakeKey = "fake-meta-key-never-persist-this"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	meta := muse.New("")
	meta.BaseURL = srv.URL
	keys := newRuntimeKeys(meta)
	keys.SetModelAPIKey(fakeKey)
	if !keys.ModelAPIKeySet() {
		t.Fatal("key status should report configured")
	}
	if _, err := meta.Chat(context.Background(), "planner", []core.Msg{core.Text("user", "hello")}, nil); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
}

func TestCloudModelSelectionStartsWithoutAPIKey(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	meta := muse.New("")
	keys := newRuntimeKeys(meta)
	reg := models.Defaults(filepath.Join(t.TempDir(), "glimmer.gguf"))
	selected, err := pick(context.Background(), reg, meta, t.TempDir(), keys)
	if err != nil {
		t.Fatalf("selection without API key failed: %v", err)
	}
	if selected.planner == nil {
		t.Fatal("default planner should be selected without an API key")
	}
	s := &settings{reg: reg, keys: keys}
	if !s.NeedsAPIKey() {
		t.Fatal("cloud Meta role without a key should ask for a key in Settings")
	}
}

func TestProbeRedactsKeyEchoedByModelServer(t *testing.T) {
	const fakeKey = "fake-probe-key-never-persist-this"
	var gotAuth string
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"rejected ` + fakeKey + `"}`))
	}))
	defer srv.Close()

	s := &settings{path: filepath.Join(t.TempDir(), "models.json")}
	caps := s.Probe(context.Background(), models.Backend{
		Name: "Protected server", Kind: "compatible", BaseURL: srv.URL, Model: "test-model",
	}, fakeKey)
	if gotAuth != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if strings.Contains(caps.Note, fakeKey) {
		t.Fatal("probe note exposed the API key")
	}
	if strings.Contains(logs.String(), fakeKey) {
		t.Fatal("probe logged the API key")
	}
}

func TestAddModelKeyIsNotReturnedOrSaved(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "")
	const fakeKey = "fake-add-key-never-persist-this"
	var gotAuth string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"rejected ` + fakeKey + `"}`))
	}))
	defer modelServer.Close()

	keys := newRuntimeKeys(muse.New(""))
	path := filepath.Join(t.TempDir(), "models.json")
	settings := &settings{path: path, keys: keys, apply: func(context.Context, models.Registry) error { return nil }}
	web := server.New(func(core.Sink) *agent.Agent { return &agent.Agent{} })
	web.Models = settings
	body, err := json.Marshal(map[string]any{
		"add":     map[string]any{"name": "Private endpoint", "kind": "compatible", "base_url": modelServer.URL, "model": "test-model"},
		"api_key": fakeKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/models", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://localhost")
	resp := httptest.NewRecorder()
	web.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
	if gotAuth != "Bearer "+fakeKey {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	if strings.Contains(resp.Body.String(), fakeKey) {
		t.Fatal("models response exposed the API key")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), fakeKey) {
		t.Fatal("API key was written to models.json")
	}
}
