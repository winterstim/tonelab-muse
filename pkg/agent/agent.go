// Package agent is the Muse tool loop: it turns a request in words into a
// verified change in the DAW. Muse decides; the DAW and the measurements
// keep it honest. It reaches everything through pkg/core ports.
package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Deps are the ports the agent works through. Renderer, Measurer and
// Visualizer enable listen; Capturer enables learn_plugin. Store is optional.
type Deps struct {
	DAW        core.DAW
	LLM        core.LLM
	Renderer   core.Renderer
	Measurer   core.Measurer
	Visualizer core.Visualizer
	Capturer   core.Capturer
	Store      core.Store
	Sink       core.Sink
	Snapshots  core.Snapshots   // optional: named tones
	Mix        core.MixAnalyzer // optional: enables check_mix
	Chain      core.ChainEditor // optional: add and remove plugins
	Taste      core.TasteStore  // optional: learns from undos and tweaks
	Segmenter  core.Segmenter   // optional: finds knobs in a photo of real gear
	// Eyes looks at plugin windows when the planner cannot. Optional:
	// nil = the planner looks itself.
	Eyes core.LLM
}

// Config chooses models and limits.
type Config struct {
	Planner     string        // model id, from the model registry's planner role
	EyesModel   string        // model id for Deps.Eyes
	NoImages    bool          // the planner cannot read images: send it numbers only
	MaxSteps    int           // tool-loop steps per request
	RenderSecs  float64       // length of each listen render
	SettleDelay time.Duration // wait after a plan before rendering
}

func (c Config) withDefaults() Config {
	if c.MaxSteps == 0 {
		c.MaxSteps = 16
	}
	if c.RenderSecs == 0 {
		c.RenderSecs = 8
	}
	return c
}

// Agent keeps one session: stored plans, the undo history and the last
// render, so listen can compare before and after.
type Agent struct {
	d   Deps
	cfg Config

	mu         sync.Mutex
	undoMu     sync.Mutex
	requestMu  sync.Mutex
	mode       Mode
	plans      map[string]*core.Plan
	nextID     int
	history    []applied // newest last
	recorded   map[int]core.RecordedClip
	lastWAV    string
	chains     map[int][]core.FX
	cat        []core.CatalogEntry // installed plugins, read once
	session    string
	photo      string             // photo of the person's real gear, for read_photo
	reference  string             // WAV the user wants to sound like
	refMeasure core.Measure       // its bands
	request    string             // the request being handled
	snapped    bool               // its "Before" snapshot is saved
	readOnly   map[string]bool    // plugins that kept none of a write this session
	allowed    map[string]bool    // the user's one-time yes per kind this session: "reload", "program", "session"
	reopened   map[string]bool    // plugins whose project reopen was tried (kept across runs)
	reopenFail map[string]bool    // plugins whose reopen was tried and did not make the write stick
	chainSeen  map[string]string  // scope/track -> fingerprint of the chain last sent to that conversation
	calls      map[string]callRec // identical calls already made in this conversation
	subCount   int                // subagents started this request, for their scope names
	startLevel *float64           // loudness before this request changed anything
	rounds     []map[string]any   // visual convergence tracker history
}

// applied is a plan as it landed, with the values it replaced.
type applied struct {
	Plan            *core.Plan
	Track           int
	Previous        map[core.ParamID]float64
	PreviousByTrack map[int]map[core.ParamID]float64
	NamesByTrack    map[int]map[core.ParamID]string
	SessionBefore   []byte
	Grouped         bool
	UndoLabel       string
	Names           map[core.ParamID]string // "Plugin / Control", for taste
	checked         bool                    // tweaks after it already counted
}

func New(d Deps, cfg Config) *Agent {
	return &Agent{d: d, cfg: cfg.withDefaults(), plans: map[string]*core.Plan{}, chains: map[int][]core.FX{},
		session: time.Now().Format("20060102-150405")}
}

// Mode decides whether apply_plan runs inside the loop.
type Mode string

const (
	Auto    Mode = "auto"    // plan, apply, listen, explain
	Preview Mode = "preview" // stop after propose_tone; the UI applies
	Turbo   Mode = "turbo"   // full autopilot: build chains, swap any plugin/preset with zero prompts
)

func (a *Agent) currentMode() Mode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) undoActionGrouper() core.UndoActionGrouper {
	if g, ok := a.d.DAW.(core.UndoActionGrouper); ok && g.SupportsUndoActionGrouping() {
		return g
	}
	if g, ok := a.d.Chain.(core.UndoActionGrouper); ok && g.SupportsUndoActionGrouping() {
		return g
	}
	return nil
}

// Run handles one request and returns the agent's closing explanation.
func (a *Agent) Run(ctx context.Context, request string, mode Mode) (string, error) {
	a.requestMu.Lock()
	defer a.requestMu.Unlock()
	a.emit("request", map[string]any{"text": request, "mode": mode})
	a.mu.Lock()
	a.mode, a.request, a.snapped, a.startLevel, a.rounds = mode, request, false, nil, nil
	a.chainSeen, a.subCount, a.calls = nil, 0, nil
	a.mu.Unlock()
	ctx = core.WithReloadPolicy(ctx, a.reloadPolicy)
	a.tasteFromTweaks(ctx)
	if a.d.LLM == nil {
		err := errors.New("no LLM configured")
		a.emit("error", err.Error())
		return a.finalAnswer("The assistant service is not configured, so I could not process this request."), err
	}
	msgs := []core.Msg{core.Text("system", System), core.Text("user", request)}
	if a.photo != "" {
		msgs = append(msgs, core.Text("user", "[Photo attached: use read_photo to inspect the gear and controls in the photo]"))
	}
	if taste := a.tastePrompt(); taste != "" {
		msgs = append(msgs, core.Text("user", taste))
	}
	catalog := newToolCatalog(a.tools(mode), mode)
	for step := 0; step < a.cfg.MaxSteps; step++ {
		stepTools := catalog.offered(msgs, false)
		if left := a.cfg.MaxSteps - step; left <= reserveSteps {
			if left == reserveSteps {
				msgs = append(msgs, core.Text("user", "Three steps are left. Only checks are offered now: make one final listen or check_mix, then answer. No more tones and no more delegation."))
			}
			stepTools = catalog.offered(msgs, true)
		}
		trimHistory(msgs)
		reply, err := a.d.LLM.Chat(ctx, a.cfg.Planner, msgs, stepTools)
		if err != nil {
			a.emit("error", err.Error())
			return a.finalAnswer("I couldn't finish this request because the assistant service failed or the request was canceled."), err
		}
		a.emit("usage", reply.Usage)
		if len(reply.ToolCalls) == 0 {
			answer := strings.TrimSpace(reply.Text)
			if answer == "" {
				answer = "I didn't receive a final response. The request may be incomplete; check the action results before continuing."
			}
			return a.finalAnswer(answer), nil
		}
		if reply.Text != "" {
			a.emit("thought", reply.Text)
		}
		msgs = append(msgs, core.Msg{Role: "assistant", Parts: textParts(reply.Text), ToolCalls: reply.ToolCalls})
		var attach []core.Part // images a tool wants the model to see next
		stop := false
		for _, tc := range reply.ToolCalls {
			a.emit("tool_call", map[string]any{"name": tc.Name, "args": tc.Args})
			out, handled, err := catalog.local(tc)
			var parts []core.Part
			if !handled {
				out, parts, err = a.call(ctx, tc)
			}
			if err != nil {
				out = map[string]any{"error": err.Error()}
				a.emit("tool_error", map[string]any{"name": tc.Name, "error": err.Error()})
			} else {
				a.emit("tool_result", map[string]any{"name": tc.Name, "result": out})
			}
			b := catalog.modelResult(out)
			msgs = append(msgs, core.Msg{Role: "tool", ToolCallID: tc.ID, Parts: []core.Part{{Type: "text", Text: string(b)}}})
			if tc.Name == "listen" && !requestedPicture(tc) {
				parts = textOnly(parts)
			}
			attach = append(attach, parts...)
			if mode == Preview && tc.Name == "propose_tone" && err == nil {
				stop = true
			}
		}
		if a.cfg.NoImages {
			attach = textOnly(attach)
		}
		if len(attach) > 0 {
			msgs = append(msgs, core.Msg{Role: "user", Parts: attach})
		}
		if stop {
			a.emit("text", "Plan ready for review.")
			return "Plan ready for review.", nil
		}
	}
	// The steps are used up: one last call without tools, so the run always
	// ends with a short answer for the user.
	msgs = append(msgs, core.Text("user", "You have used all your steps for this request. Do not call any more tools. In two or three sentences, tell the user what changed, what did not work and why, and what they can do next."))
	reply, err := a.d.LLM.Chat(ctx, a.cfg.Planner, msgs, nil)
	if err != nil {
		a.emit("error", err.Error())
		return a.finalAnswer("I couldn't finish this request because the assistant service failed or the request was canceled."), err
	}
	a.emit("usage", reply.Usage)
	answer := strings.TrimSpace(reply.Text)
	if answer == "" {
		answer = "I reached the step limit and didn't receive a final response. The request may be incomplete; check the action results before continuing."
	}
	return a.finalAnswer(answer), nil
}

func (a *Agent) finalAnswer(text string) string {
	a.emit("text", text)
	return text
}

// ApplyPlan applies a stored plan (the UI's "Do it" in preview mode).
func (a *Agent) ApplyPlan(ctx context.Context, id string) (any, error) {
	return a.applyPlan(ctx, id)
}

// Undo is the user's undo: it restores the newest plan and remembers that
// its direction was not wanted.
func (a *Agent) Undo(ctx context.Context) (any, error) {
	a.mu.Lock()
	var h applied
	if n := len(a.history); n > 0 {
		h = a.history[n-1]
	}
	a.mu.Unlock()
	out, err := a.undo(ctx)
	if err == nil && h.Plan != nil && !strings.HasPrefix(h.Plan.Goal, "Recall ") {
		a.tasteFromUndo(h, h.Names)
	}
	return out, err
}

// undo restores the values the newest plan replaced.
func (a *Agent) undo(ctx context.Context) (any, error) {
	a.undoMu.Lock()
	defer a.undoMu.Unlock()
	a.mu.Lock()
	if len(a.history) == 0 {
		a.mu.Unlock()
		return nil, errors.New("nothing to undo")
	}
	h := a.history[len(a.history)-1]
	a.mu.Unlock()
	if len(h.SessionBefore) > 0 {
		r, ok := a.d.DAW.(core.RequestSnapshotter)
		if !ok || !r.SupportsRequestSnapshot() {
			return nil, errors.New("the DAW cannot restore the previous full-session state")
		}
		if err := r.RestoreRequestSnapshot(ctx, h.SessionBefore); err != nil {
			return nil, err
		}
		a.popHistory(h)
		out := map[string]any{"undone": h.Plan.ID, "scope": "session", "confirmed": true}
		a.emit("undo", out)
		return out, nil
	}
	if h.Grouped && h.UndoLabel != "" {
		g, ok := a.d.DAW.(core.UndoActionGrouper)
		if ok && g.SupportsUndoActionGrouping() {
			current, err := g.UndoActionIsCurrent(ctx, h.UndoLabel)
			if err != nil {
				return nil, err
			}
			if current {
				if err := a.d.DAW.Undo(ctx); err != nil {
					return nil, err
				}
				if err := a.verifyRestored(ctx, h); err != nil {
					return nil, err
				}
				a.popHistory(h)
				out := map[string]any{"undone": h.Plan.ID, "confirmed": true, "method": "DAW undo step"}
				a.emit("undo", out)
				return out, nil
			}
		}
	}
	var restored []core.Applied
	apply := func() error {
		if len(h.PreviousByTrack) > 0 {
			for track, values := range h.PreviousByTrack {
				for id, v := range values {
					r, err := a.d.DAW.SetParam(ctx, track, id, v)
					if err != nil {
						return err
					}
					if r.Status != core.StatusConfirmed {
						return fmt.Errorf("could not confirm undo of %s on track %d", id, track)
					}
					if names := h.NamesByTrack[track]; names != nil {
						r.Name = names[id]
					}
					restored = append(restored, r)
				}
			}
		} else {
			for id, v := range h.Previous {
				r, err := a.d.DAW.SetParam(ctx, h.Track, id, v)
				if err != nil {
					return err
				}
				if r.Status != core.StatusConfirmed {
					return fmt.Errorf("could not confirm undo of %s on track %d", id, h.Track)
				}
				restored = append(restored, r)
			}
		}
		return nil
	}
	if h.Grouped {
		if err := a.withUndoAction(ctx, "Agent: restore "+h.UndoLabel, apply); err != nil {
			return nil, err
		}
	} else if err := apply(); err != nil {
		return nil, err
	}
	a.popHistory(h)
	out := map[string]any{"undone": h.Plan.ID, "restored": restored}
	a.emit("undo", out)
	return out, nil
}

func (a *Agent) popHistory(target applied) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.history) > 0 && a.history[len(a.history)-1].Plan == target.Plan {
		a.history = a.history[:len(a.history)-1]
	}
}

func (a *Agent) verifyRestored(ctx context.Context, h applied) error {
	if len(h.PreviousByTrack) > 0 {
		for track, values := range h.PreviousByTrack {
			chain, err := a.chain(ctx, track, true)
			if err != nil {
				return err
			}
			for id, want := range values {
				got, ok := find(chain, id)
				if !ok || math.Abs(got.Value-want) > 0.005 {
					return fmt.Errorf("DAW undo did not restore %s on track %d", id, track)
				}
			}
		}
		return nil
	}
	chain, err := a.chain(ctx, h.Track, true)
	if err != nil {
		return err
	}
	for id, want := range h.Previous {
		got, ok := find(chain, id)
		if !ok || math.Abs(got.Value-want) > 0.005 {
			return fmt.Errorf("DAW undo did not restore %s on track %d", id, h.Track)
		}
	}
	return nil
}

func (a *Agent) withUndoAction(ctx context.Context, label string, action func() error) (retErr error) {
	g := a.undoActionGrouper()
	if g == nil {
		return action()
	}
	if err := g.BeginUndoAction(ctx, label); err != nil {
		return err
	}
	open := true
	commit := false
	defer func() {
		if open {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if err := g.EndUndoAction(cleanupCtx, label, commit); err != nil {
				a.emit("undo_group_error", map[string]any{"action": label, "error": err.Error()})
				retErr = errors.Join(retErr, fmt.Errorf("close undo group: %w", err))
			}
		}
	}()
	if err := action(); err != nil {
		return err
	}
	if err := g.EndUndoAction(ctx, label, true); err != nil {
		return err
	}
	commit = true
	open = false
	return nil
}

func (a *Agent) emit(kind string, data any) {
	e := core.Event{At: time.Now(), Kind: kind, Data: data}
	if a.d.Sink != nil {
		a.d.Sink.Emit(e)
	}
	if a.d.Store != nil {
		a.d.Store.Append(a.session, e)
	}
}

func textParts(s string) []core.Part {
	if s == "" {
		return nil
	}
	return []core.Part{{Type: "text", Text: s}}
}

func imagePart(path string) (core.Part, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return core.Part{}, err
	}
	return core.Part{Type: "image", ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b)}, nil
}

// textOnly drops images for a planner that cannot see; the numbers in the
// tool results carry the same facts.
func textOnly(ps []core.Part) []core.Part {
	var out []core.Part
	for _, p := range ps {
		if p.Type != "image" {
			out = append(out, p)
		}
	}
	if len(out) > 0 {
		out = append(out, core.Part{Type: "text", Text: "(The image is left out: use the measured numbers.)"})
	}
	return out
}

// eyes is the model that looks at plugin windows.
func (a *Agent) eyes() (core.LLM, string) {
	if a.d.Eyes != nil && a.cfg.EyesModel != "" {
		return a.d.Eyes, a.cfg.EyesModel
	}
	if a.d.Eyes != nil {
		return a.d.Eyes, a.cfg.Planner
	}
	return a.d.LLM, a.cfg.Planner
}
