// Package server is the local web UI: one page whose main view is the
// agent's live action log, streamed over Server-Sent Events.
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/internal/models"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	_ "golang.org/x/image/webp"
)

//go:embed index.html
var page []byte

//go:embed logo.svg
var logo []byte

// Server runs one agent session and fans its events out to every open page.
type Server struct {
	Images, Renders string // directories served under /files/
	Refs            string // uploaded reference recordings, also served
	// Status checks the DAW; Profiles lists learned plugins. Both optional.
	Status   func(context.Context) error
	Profiles func() ([]core.Profile, error)
	// Bridge is the one-time permission for plugin changes. Optional.
	Bridge interface {
		Enabled() bool
		Enable() (restart bool, err error)
		Disable() error
	}
	// Taste is what the agent learned from undos and tweaks. Optional.
	Taste core.TasteStore
	// Snapshots lists saved tones. Optional.
	Snapshots func() ([]core.Snapshot, error)
	// Voice turns push-to-talk audio into a request. Optional.
	Voice core.Transcriber

	lastOK bool

	agent *agent.Agent
	build func(core.Sink) *agent.Agent
	// Models shows and changes which model does what. Optional.
	Models ModelSettings
	// DAWs switches the DAW and teaches the app new ones. Optional.
	DAWs DAWSettings
	mu   sync.Mutex
	busy bool
	subs map[chan core.Event]bool
	log  []core.Event // replayed to pages that connect late
}

// New builds the agent with the server as its event sink.
func New(build func(core.Sink) *agent.Agent) *Server {
	s := &Server{subs: map[chan core.Event]bool{}, build: build}
	s.agent = build(s)
	return s
}

// Vocabulary is what the current agent has read: plugin and control names.
func (s *Server) Vocabulary() []string {
	s.mu.Lock()
	a := s.agent
	s.mu.Unlock()
	return a.Vocabulary()
}

// Rebuild makes a new agent from the current settings (after the user
// changed models). Refused while the agent is working.
func (s *Server) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return errors.New("the agent is working; try again when it is done")
	}
	s.agent = s.build(s)
	return nil
}

func (s *Server) Emit(e core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // a stalled page misses events rather than stalling the agent
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(logo)
	})
	mux.HandleFunc("POST /api/ask", s.ask)
	mux.HandleFunc("POST /api/voice", s.voice)
	mux.HandleFunc("POST /api/apply", s.apply)
	mux.HandleFunc("GET /api/snapshots", s.snapshots)
	mux.HandleFunc("GET /api/models", s.models)
	mux.HandleFunc("POST /api/models", s.models)
	mux.HandleFunc("GET /api/chain", s.chain)
	mux.HandleFunc("GET /api/daws", s.daws)
	mux.HandleFunc("POST /api/daws", s.daws)
	mux.HandleFunc("GET /api/taste", s.taste)
	mux.HandleFunc("DELETE /api/taste", s.taste)
	mux.HandleFunc("GET /api/bridge", s.bridge)
	mux.HandleFunc("POST /api/bridge", s.bridge)
	mux.HandleFunc("POST /api/allow", s.allow)
	mux.HandleFunc("POST /api/recall", s.recall)
	mux.HandleFunc("POST /api/undo", s.undo)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/profiles", s.profiles)
	if s.Images != "" {
		mux.Handle("GET /files/images/", http.StripPrefix("/files/images/", http.FileServer(http.Dir(s.Images))))
	}
	if s.Refs != "" {
		mux.Handle("GET /files/refs/", http.StripPrefix("/files/refs/", http.FileServer(http.Dir(s.Refs))))
		mux.HandleFunc("POST /api/reference", s.reference)
		mux.HandleFunc("POST /api/photo", s.photo)
	}
	if s.Renders != "" {
		mux.Handle("GET /files/renders/", http.StripPrefix("/files/renders/", http.FileServer(http.Dir(s.Renders))))
	}
	return mux
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(sh)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// claim marks the agent busy; the page shows one request at a time.
func (s *Server) claim(w http.ResponseWriter) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		http.Error(w, "the agent is still working on the last request", http.StatusConflict)
		return false
	}
	s.busy = true
	return true
}

func (s *Server) release() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	s.Emit(core.Event{At: time.Now(), Kind: "idle"})
}

func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string     `json:"text"`
		Mode agent.Mode `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Text == "" {
		http.Error(w, "send {\"text\": \"...\"}", http.StatusBadRequest)
		return
	}
	if in.Mode != agent.Preview && in.Mode != agent.Turbo {
		in.Mode = agent.Auto
	}
	if !s.claim(w) {
		return
	}
	go func() {
		defer s.release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.agent.Run(ctx, in.Text, in.Mode) // errors arrive as events
	}()
	w.WriteHeader(http.StatusAccepted)
}

// voice takes a WAV body, transcribes it and runs the words as a request.
// The transcript goes back to the page so the user sees what was heard.
func (s *Server) voice(w http.ResponseWriter, r *http.Request) {
	if s.Voice == nil {
		http.Error(w, "voice is not set up", http.StatusNotImplemented)
		return
	}
	wav, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil || len(wav) < 44 {
		http.Error(w, "send a WAV recording", http.StatusBadRequest)
		return
	}
	mode := agent.Mode(r.URL.Query().Get("mode"))
	if mode != agent.Preview && mode != agent.Turbo {
		mode = agent.Auto
	}
	if !s.claim(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	text, err := s.Voice.Transcribe(ctx, wav)
	cancel()
	if err != nil || text == "" {
		s.release()
		if err == nil {
			err = errors.New("heard nothing; tap the mic, speak, tap again")
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	go func() {
		defer s.release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.agent.Run(ctx, text, mode)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"text": text})
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.PlanID == "" {
		http.Error(w, "send {\"plan_id\": \"...\"}", http.StatusBadRequest)
		return
	}
	s.run(w, func(ctx context.Context) (any, error) { return s.agent.ApplyPlan(ctx, in.PlanID) }, "applied")
}

func (s *Server) undo(w http.ResponseWriter, r *http.Request) {
	s.run(w, s.agent.Undo, "")
}

func (s *Server) run(w http.ResponseWriter, f func(context.Context) (any, error), kind string) {
	s.runFor(w, f, kind, time.Minute)
}

// runFor is run with a longer limit, for work that waits on the person.
func (s *Server) runFor(w http.ResponseWriter, f func(context.Context) (any, error), kind string, limit time.Duration) {
	if !s.claim(w) {
		return
	}
	defer s.release()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := f(ctx)
	if err != nil {
		s.Emit(core.Event{At: time.Now(), Kind: "error", Data: err.Error()})
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if kind != "" {
		s.Emit(core.Event{At: time.Now(), Kind: kind, Data: out})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan core.Event, 256)
	s.mu.Lock()
	backlog := append([]core.Event(nil), s.log...)
	s.subs[ch] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	write := func(e core.Event) bool {
		b, err := json.Marshal(e)
		if err != nil {
			return true
		}
		if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	for _, e := range backlog {
		if !write(e) {
			return
		}
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !write(e) {
				return
			}
		case <-tick.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// status answers from the DAW when idle; while the agent works it repeats
// the last answer instead of probing in the middle of a plan.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	busy, ok := s.busy, s.lastOK
	s.mu.Unlock()
	if !busy && s.Status != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		ok = s.Status(ctx) == nil
		cancel()
		s.mu.Lock()
		s.lastOK = ok
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	out := map[string]any{"connected": ok}
	if s.DAWs != nil {
		cur, daws := s.DAWs.List()
		out["daw"] = cur
		for _, d := range daws {
			if d.ID == cur {
				out["label"] = d.Label
			}
		}
	}
	json.NewEncoder(w).Encode(out)
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Plugin  string `json:"plugin"`
		Learned int    `json:"learned"`
	}
	out := []row{}
	if s.Profiles != nil {
		ps, _ := s.Profiles()
		for _, p := range ps {
			out = append(out, row{p.Plugin, len(p.Map)})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Name     string `json:"name"`
		Controls int    `json:"controls"`
		Saved    string `json:"saved"`
	}
	out := []row{}
	if s.Snapshots != nil {
		ss, _ := s.Snapshots()
		for _, sn := range ss {
			out = append(out, row{sn.Name, len(sn.Values), sn.Saved.Format("15:04")})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) recall(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		http.Error(w, "send {\"name\": \"...\"}", http.StatusBadRequest)
		return
	}
	s.run(w, func(ctx context.Context) (any, error) { return s.agent.RecallSnapshot(ctx, in.Name) }, "applied")
}

// reference takes an uploaded recording (raw body, name in ?name=), turns
// anything that is not WAV into WAV with macOS's afconvert, and makes it
// the session's reference.
func (s *Server) reference(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Query().Get("name"))
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".wav", ".mp3", ".m4a", ".aif", ".aiff", ".flac", ".caf":
	default:
		http.Error(w, "send a WAV, MP3, M4A, AIFF or FLAC file", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.Refs, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// keep the user's name visible, made safe for a path and a URL
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimSuffix(name, filepath.Ext(name)))
	stem := fmt.Sprintf("%s-%d", clean, time.Now().Unix())
	raw := filepath.Join(s.Refs, stem+ext)
	f, err := os.Create(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, io.LimitReader(r.Body, 200<<20))
	f.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wav := raw
	if ext != ".wav" { // the page sends WAV; a Mac also converts what else comes in
		if runtime.GOOS != "darwin" {
			os.Remove(raw)
			http.Error(w, "send a WAV file", http.StatusBadRequest)
			return
		}
		wav = filepath.Join(s.Refs, stem+".wav")
		if out, err := exec.CommandContext(r.Context(), "afconvert", "-f", "WAVE", "-d", "LEI16", raw, wav).CombinedOutput(); err != nil {
			http.Error(w, "could not read that file: "+strings.TrimSpace(string(out)), http.StatusBadRequest)
			return
		}
		os.Remove(raw)
	}
	out, err := s.agent.SetReference(wav)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"name": name, "result": out})
}

// photo: POST ?name=, the body a photo of the person's amp or pedal (JPEG,
// PNG, HEIC, WebP). It is kept as JPEG for read_photo.
func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	ext := strings.ToLower(filepath.Ext(filepath.Base(r.URL.Query().Get("name"))))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".heic", ".heif", ".webp":
	default:
		http.Error(w, "send a JPEG, PNG, HEIC or WebP photo", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.Refs, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// the upload and the converted JPEG get different names: a JPEG upload
	// converted onto its own path would be truncated, then removed
	stamp := time.Now().UnixNano()
	raw := filepath.Join(s.Refs, fmt.Sprintf("upload-%d%s", stamp, ext))
	f, err := os.Create(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, io.LimitReader(r.Body, 40<<20))
	f.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// whatever came in (JPEG, PNG, WebP, a misnamed file), keep a JPEG;
	// a Mac also converts what Go cannot read, like HEIC from a phone
	path := filepath.Join(s.Refs, fmt.Sprintf("photo-%d.jpg", stamp))
	if err := toJPEG(r.Context(), raw, path); err != nil {
		os.Remove(raw)
		http.Error(w, "could not read that photo: "+err.Error(), http.StatusBadRequest)
		return
	}
	os.Remove(raw)
	s.mu.Lock()
	a := s.agent
	s.mu.Unlock()
	a.SetPhoto(path)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"photo": filepath.Base(path)})
}

// allow is the user's one-time yes from a consent card in Default mode:
// POST {"kind": K} for the current session, K one of reload, program, probe,
// session, record or preset_save.
func (s *Server) allow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Kind != "reload" && in.Kind != "program" && in.Kind != "probe" && in.Kind != "session" && in.Kind != "record" && in.Kind != "preset_save") {
		http.Error(w, `kind must be "reload", "program", "probe", "session", "record" or "preset_save"`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	a := s.agent
	s.mu.Unlock()
	if a == nil {
		http.Error(w, "no session yet", http.StatusConflict)
		return
	}
	a.Allow(in.Kind)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"allowed": in.Kind})
}

// bridge reports or changes the user's permission for plugin changes.
// POST {"enable": true} is the user's yes from the consent card.
func (s *Server) bridge(w http.ResponseWriter, r *http.Request) {
	if s.Bridge == nil {
		http.Error(w, "plugin changes are not available", http.StatusNotImplemented)
		return
	}
	out := map[string]any{}
	if r.Method == http.MethodPost {
		var in struct {
			Enable bool `json:"enable"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Enable {
			restart, err := s.Bridge.Enable()
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			out["restart"] = restart
		} else if err := s.Bridge.Disable(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	out["enabled"] = s.Bridge.Enabled()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// taste shows what the agent learned; DELETE forgets all of it.
func (s *Server) taste(w http.ResponseWriter, r *http.Request) {
	if s.Taste == nil {
		http.Error(w, "taste is not kept", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.Taste.SaveTaste(core.Taste{}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	t, err := s.Taste.Taste()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t.Leanings == nil {
		t.Leanings = []core.Leaning{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

// ModelSettings is the model registry as the app keeps it.
type ModelSettings interface {
	Get() models.Registry
	// Apply saves the registry, starts what it needs and rebuilds the agent.
	Apply(ctx context.Context, r models.Registry) error
	Probe(ctx context.Context, b models.Backend) models.Caps
}

// models: GET the registry with what does not fit; POST one change:
// {"mode": "cloud"|"private"}, {"roles": {...}} or {"add": {...backend}}.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if s.Models == nil {
		http.Error(w, "model settings are not available", http.StatusNotImplemented)
		return
	}
	reg := s.Models.Get()
	if r.Method == http.MethodPost {
		var in struct {
			Mode  string          `json:"mode"`
			Roles *models.Roles   `json:"roles"`
			Add   *models.Backend `json:"add"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case in.Add != nil:
			b := *in.Add
			b.Caps = s.Models.Probe(r.Context(), b) // proven, not assumed
			if err := reg.Put(b); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		case in.Roles != nil:
			reg.Roles, reg.Mode = *in.Roles, "custom"
		case in.Mode != "":
			if in.Mode == "private" { // a local model is tested before it gets a job
				for i, b := range reg.Backends {
					if b.Local && b.Caps.Checked.IsZero() {
						reg.Backends[i].Caps = s.Models.Probe(r.Context(), b)
					}
				}
			}
			reg.SetMode(in.Mode)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute) // a model may need to load
		defer cancel()
		if err := s.Models.Apply(ctx, reg); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		reg = s.Models.Get()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"registry": reg, "problems": reg.Check()})
}

// DAWSettings is the DAW the agent works in, and the ones it has learned.
type DAWSettings interface {
	List() (current string, daws []DAWInfo)
	// Use connects to another DAW; the agent is rebuilt after.
	Use(ctx context.Context, name string) error
	// Onboard learns a new DAW from its docs (text, or a link to fetch).
	Onboard(ctx context.Context, name, docs, url string, progress func(string)) error
}

// DAWInfo is one DAW the app can connect to, with the name it shows.
type DAWInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// daws: GET the current DAW and the known ones; POST {"use": name} to
// switch, or {"onboard": name, "docs"|"url": ...} to learn one and switch.
func (s *Server) daws(w http.ResponseWriter, r *http.Request) {
	if s.DAWs == nil {
		http.Error(w, "DAW settings are not available", http.StatusNotImplemented)
		return
	}
	list := func(ctx context.Context) (any, error) {
		cur, daws := s.DAWs.List()
		return map[string]any{"current": cur, "daws": daws}, nil
	}
	if r.Method != http.MethodPost {
		out, _ := list(r.Context())
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
		return
	}
	var in struct{ Use, Onboard, Docs, URL string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	use := func(ctx context.Context, name string) (any, error) {
		if err := s.DAWs.Use(ctx, name); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.agent = s.build(s)
		s.mu.Unlock()
		return list(ctx)
	}
	if in.Onboard == "" {
		s.runFor(w, func(ctx context.Context) (any, error) { return use(ctx, in.Use) }, "daw", 20*time.Second)
		return
	}
	s.runFor(w, func(ctx context.Context) (any, error) {
		say := func(t string) { s.Emit(core.Event{At: time.Now(), Kind: "onboard", Data: t}) }
		say("Reading the docs for " + in.Onboard + " and writing a command map…")
		if err := s.DAWs.Onboard(ctx, in.Onboard, in.Docs, in.URL, say); err != nil {
			return nil, err
		}
		return use(ctx, strings.ToLower(strings.TrimSpace(in.Onboard)))
	}, "daw", 10*time.Minute)
}

// chain: GET ?track=N, the track's plugins with readable values (for the
// tone card). Refused while the agent works, to keep the DAW to one voice.
func (s *Server) chain(w http.ResponseWriter, r *http.Request) {
	track, err := strconv.Atoi(r.URL.Query().Get("track"))
	if err != nil || track < 1 {
		http.Error(w, "send ?track=N", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	busy, a := s.busy, s.agent
	s.mu.Unlock()
	if busy {
		http.Error(w, "the agent is working; try again when it is done", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ch, err := a.Chain(ctx, track)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ch)
}

func toJPEG(ctx context.Context, src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, derr := image.Decode(f)
	f.Close()
	if derr != nil {
		if runtime.GOOS != "darwin" {
			return errors.New("send a JPEG, PNG or WebP photo")
		}
		if out, err := exec.CommandContext(ctx, "sips", "-s", "format", "jpeg", src, "--out", dst).CombinedOutput(); err != nil {
			return errors.New(strings.TrimSpace(string(out)))
		}
		return nil
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, img, &jpeg.Options{Quality: 92}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
