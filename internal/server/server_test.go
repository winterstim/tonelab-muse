package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

type dummyLLM struct{}

func (d *dummyLLM) Chat(_ context.Context, _ string, _ []core.Msg, _ []core.Tool) (core.Reply, error) {
	return core.Reply{Text: "done"}, nil
}

type dummyVoice struct{}

func (d *dummyVoice) Transcribe(_ context.Context, _ []byte) (string, error) {
	return "give me more gain", nil
}

type apiKeyStub struct{ key string }

func (s *apiKeyStub) SetModelAPIKey(key string) { s.key = key }
func (s *apiKeyStub) ModelAPIKeySet() bool      { return s.key != "" }

type dummyDAW struct{}

func (d *dummyDAW) Tracks(context.Context) ([]core.Track, error) { return nil, nil }
func (d *dummyDAW) FXChain(context.Context, int) ([]core.FX, error) {
	return nil, nil
}
func (d *dummyDAW) SetParam(context.Context, int, core.ParamID, float64) (core.Applied, error) {
	return core.Applied{}, nil
}
func (d *dummyDAW) SetTrack(context.Context, int, string, float64) (float64, error) { return 0, nil }
func (d *dummyDAW) Undo(context.Context) error                                      { return nil }
func (d *dummyDAW) Alive(context.Context) error                                     { return nil }

type modeCaptureSink struct {
	mu    sync.Mutex
	modes []agent.Mode
}

func (s *modeCaptureSink) Emit(e core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Kind == "request" {
		if m, ok := e.Data.(map[string]any); ok {
			if mode, ok := m["mode"].(agent.Mode); ok {
				s.modes = append(s.modes, mode)
			}
		}
	}
}

func (s *modeCaptureSink) last() agent.Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.modes) == 0 {
		return ""
	}
	return s.modes[len(s.modes)-1]
}

func TestServerAskModes(t *testing.T) {
	sink := &modeCaptureSink{}
	llm := &dummyLLM{}
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{
			DAW:  &dummyDAW{},
			LLM:  llm,
			Sink: sink,
		}, agent.Config{})
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	tests := []struct {
		sendMode agent.Mode
		wantMode agent.Mode
	}{
		{sendMode: agent.Turbo, wantMode: agent.Turbo},
		{sendMode: agent.Preview, wantMode: agent.Preview},
		{sendMode: agent.Auto, wantMode: agent.Auto},
		{sendMode: "something_else", wantMode: agent.Auto},
		{sendMode: "", wantMode: agent.Auto},
	}

	for _, tt := range tests {
		body, _ := json.Marshal(map[string]any{
			"text": "test prompt",
			"mode": tt.sendMode,
		})
		resp, err := http.Post(ts.URL+"/api/ask", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/ask: %v", err)
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
		}
		// wait for agent.Run goroutine to emit request
		for i := 0; i < 20; i++ {
			time.Sleep(10 * time.Millisecond)
			if s := sink.last(); s == tt.wantMode {
				break
			}
		}
		if got := sink.last(); got != tt.wantMode {
			t.Errorf("send mode %q: got %q, want %q", tt.sendMode, got, tt.wantMode)
		}
	}
}

func TestAPIKeyEndpointIsLocalAndNeverEchoesKey(t *testing.T) {
	const fakeKey = "fake-key-never-persist-this"
	keys := &apiKeyStub{}
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{DAW: &dummyDAW{}, LLM: &dummyLLM{}, Sink: s}, agent.Config{})
	})
	srv.APIKeys = keys
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/key", strings.NewReader(`{"api_key":"`+fakeKey+`"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://localhost")
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), fakeKey) || strings.TrimSpace(resp.Body.String()) != `{"key_set":true}` {
		t.Fatalf("response leaked key or returned extra data: %q", resp.Body.String())
	}
	if keys.key != fakeKey {
		t.Fatal("key was not stored in the runtime key store")
	}

	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://localhost/api/key", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	srv.Handler().ServeHTTP(resp, req)
	if strings.Contains(resp.Body.String(), fakeKey) || strings.TrimSpace(resp.Body.String()) != `{"key_set":true}` {
		t.Fatalf("status response leaked key or returned extra data: %q", resp.Body.String())
	}

	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://localhost/api/key", strings.NewReader(`{"api_key":"changed"}`))
	req.RemoteAddr = "198.51.100.8:12345"
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden || keys.key != fakeKey {
		t.Fatalf("remote request status=%d changed-key=%v", resp.Code, keys.key != fakeKey)
	}
}

func TestMissingModelKeyUsesSettingsMessage(t *testing.T) {
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{DAW: &dummyDAW{}, LLM: &dummyLLM{}, Sink: s}, agent.Config{})
	})
	srv.NeedsAPIKey = func() bool { return true }
	req := httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader(`{"text":"make it brighter"}`))
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusPreconditionRequired || strings.TrimSpace(resp.Body.String()) != "Add your Model API key in Settings" {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
}

func TestServerVoiceModes(t *testing.T) {
	sink := &modeCaptureSink{}
	llm := &dummyLLM{}
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{
			DAW:  &dummyDAW{},
			LLM:  llm,
			Sink: sink,
		}, agent.Config{})
	})
	srv.Voice = &dummyVoice{}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	fakeWav := make([]byte, 100)

	tests := []struct {
		param    string
		wantMode agent.Mode
	}{
		{param: "turbo", wantMode: agent.Turbo},
		{param: "preview", wantMode: agent.Preview},
		{param: "auto", wantMode: agent.Auto},
		{param: "unknown", wantMode: agent.Auto},
		{param: "", wantMode: agent.Auto},
	}

	for _, tt := range tests {
		url := ts.URL + "/api/voice"
		if tt.param != "" {
			url += "?mode=" + tt.param
		}
		resp, err := http.Post(url, "audio/wav", bytes.NewReader(fakeWav))
		if err != nil {
			t.Fatalf("POST /api/voice: %v", err)
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
		}
		for i := 0; i < 20; i++ {
			time.Sleep(10 * time.Millisecond)
			if s := sink.last(); s == tt.wantMode {
				break
			}
		}
		if got := sink.last(); got != tt.wantMode {
			t.Errorf("voice param %q: got %q, want %q", tt.param, got, tt.wantMode)
		}
	}
}

func TestIndexHTMLHasAutonomyButtons(t *testing.T) {
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{DAW: &dummyDAW{}}, agent.Config{})
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	html := buf.String()

	if !strings.Contains(html, `data-mode="preview"`) || !strings.Contains(html, "Review</button>") {
		t.Error("missing Review button")
	}
	if !strings.Contains(html, `data-mode="auto"`) || !strings.Contains(html, "Default</button>") {
		t.Error("missing Default button")
	}
	if !strings.Contains(html, `data-mode="turbo"`) || !strings.Contains(html, "Turbo</button>") {
		t.Error("missing Turbo button")
	}
}

func TestIndexHTMLHasConvergenceChartAndTools(t *testing.T) {
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{DAW: &dummyDAW{}}, agent.Config{})
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	html := buf.String()

	if !strings.Contains(html, ".convergence-chart") {
		t.Error("missing .convergence-chart CSS class")
	}
	if !strings.Contains(html, "function convergenceChart(") {
		t.Error("missing convergenceChart JS function")
	}
	for _, tool := range []string{
		"list_plugin_models", "set_plugin_model", "manage_track", "insert_media", "route_send", "set_transport", "delegate_track_task",
	} {
		if !strings.Contains(html, tool+":") {
			t.Errorf("missing sentence definition for tool %q", tool)
		}
	}
	if !strings.Contains(html, "function actionSvg(") {
		t.Error("missing actionSvg function")
	}
	if !strings.Contains(html, ".subagent-card") {
		t.Error("missing .subagent-card CSS class")
	}
	if !strings.Contains(html, "meta-models/muse-spark-open-1026") {
		t.Error("missing meta-models/muse-spark-open-1026 reference")
	}
}

// A JPEG upload must survive the handler: the converted photo is kept for
// read_photo, not truncated and removed because it shared the upload's name.
func TestServerPhotoJPEGUploadIsKept(t *testing.T) {
	refs := t.TempDir()
	srv := New(func(s core.Sink) *agent.Agent {
		return agent.New(agent.Deps{DAW: &dummyDAW{}, LLM: &dummyLLM{}, Sink: s}, agent.Config{})
	})
	srv.Refs = refs
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/photo?name=pedal.jpg", "image/jpeg", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Photo string `json:"photo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, decode %v", resp.StatusCode, err)
	}
	if _, err := os.Stat(filepath.Join(refs, out.Photo)); err != nil {
		t.Fatalf("photo %s was not kept: %v", out.Photo, err)
	}
	if entries, _ := os.ReadDir(refs); len(entries) != 1 {
		t.Fatalf("refs should hold only the photo, got %d files", len(entries))
	}
}
