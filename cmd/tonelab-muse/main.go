// Command tonelab-muse sets a guitar tone in REAPER from words, with Muse.
//
//	tonelab-muse ask "give me a tight modern metal tone"
//	tonelab-muse serve            # web UI on http://localhost:7400
//
// Environment: MODEL_API_KEY (required), MUSE_MODEL (overrides the
// planner's Meta model id), TONELAB_BUDGET (tokens per run, default 400000).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/oscmap"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/internal/eyes"
	"github.com/winterstim/tonelab-muse/internal/models"
	"github.com/winterstim/tonelab-muse/internal/server"
	"github.com/winterstim/tonelab-muse/internal/store"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/ears"
	"github.com/winterstim/tonelab-muse/pkg/muse"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "conformance" {
		os.Exit(runConformanceCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	listen := flag.String("osc-listen", "127.0.0.1:9000", "where REAPER sends feedback")
	send := flag.String("osc-send", "127.0.0.1:8000", "where REAPER listens")
	addr := flag.String("addr", "127.0.0.1:7400", "web UI address (serve)")
	preview := flag.Bool("preview", false, "ask: stop after the plan")
	turbo := flag.Bool("turbo", false, "ask: full autopilot mode")
	ref := flag.String("ref", "", "ask: a WAV to sound like")
	photo := flag.String("photo", "", "ask: a photo of real gear to sound like")
	modelName := flag.String("model", "", "planner model name from models.json")
	dawMap := flag.String("daw", "", "a DAW command map (from onboard) instead of REAPER")
	docs := flag.String("docs", "", "onboard: the DAW's remote-control documentation")
	demo := flag.Bool("demo", false, "run with a built-in simulated multi-track DAW studio (no REAPER or Ableton required)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: tonelab-muse [flags] ask \"<request>\" | serve | onboard <name> | conformance --daw fake|reaper|ableton")
		flag.PrintDefaults()
	}
	flag.Parse()
	cmd := ""
	if flag.NArg() >= 1 {
		cmd = flag.Arg(0)
	} else if *demo {
		cmd = "serve"
	} else {
		flag.Usage()
		os.Exit(2)
	}
	key := os.Getenv("MODEL_API_KEY")
	home, _ := os.UserHomeDir()
	data := filepath.Join(home, ".tonelab-muse")

	llm := muse.New(key)
	usage := newUsageLog(llm)
	defer usage.close()
	llm.Budget = 400000
	if b, err := strconv.Atoi(os.Getenv("TONELAB_BUDGET")); err == nil {
		llm.Budget = b
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	reg, err := models.Load(filepath.Join(data, "models.json"), glimmerFile(home))
	if err != nil {
		fail(err)
	}
	if *modelName != "" {
		reg.Roles.Planner = *modelName
	}
	ms, err := pick(ctx, reg, llm, data)
	if err != nil {
		fail(err)
	}
	if cmd == "onboard" { // onboard <name>: Muse learns a new DAW from its docs
		if pb, ok := reg.Get(reg.Roles.Planner); ok && pb.Kind == "meta" && key == "" {
			fail(fmt.Errorf("MODEL_API_KEY is not set"))
		}
		if flag.NArg() < 2 || *docs == "" {
			fail(fmt.Errorf("usage: onboard -docs <file> <name>"))
		}
		onboard(ctx, ms.planner, ms.plannerModel, *docs, filepath.Join(data, "daws", flag.Arg(1)+".json"))
		return
	}
	var cur *studio
	if *demo {
		cur, err = openDemoStudio(data)
	} else {
		cur, err = openStudio(*dawMap, data, *listen, *send)
	}
	if err != nil {
		fail(err)
	}
	defer func() { cur.close() }()
	st := &store.Store{Dir: data}
	deps := agent.Deps{
		LLM:        ms.planner,
		Eyes:       ms.eyes,
		Measurer:   ears.Meter{},
		Visualizer: &ears.Painter{Dir: filepath.Join(data, "images")},
		Store:      st,
		Snapshots:  st,
		Mix:        ears.Meter{},
		Taste:      st,
		Segmenter:  muse.Segmenter{Client: llm},
	}
	cfg := agent.Config{Planner: ms.plannerModel, EyesModel: ms.eyesModel, NoImages: ms.blind}
	// wire points the agent at a DAW; the REAPER-only pieces go with REAPER.
	wire := func(s *studio) {
		deps.DAW = s.daw
		deps.Capturer, deps.Renderer = nil, nil
		deps.Chain = s.chain
		if s.rp != nil && !*demo { // plugin windows, plugin bridge
			deps.Capturer = &eyes.Capturer{App: reaper.App, Title: s.rp.WindowTitle, Open: s.rp, Dir: filepath.Join(data, "images")}
		} else if d, ok := s.daw.(*oscmap.DAW); ok && d.M.Shows() && d.M.App != "" { // a mapped DAW that can show a device
			deps.Capturer = &eyes.Capturer{Bundle: d.M.App, Open: d, Dir: filepath.Join(data, "images")}
		}
		if s.render != nil {
			deps.Renderer = s.render
		}
	}
	wire(cur)

	if err := cur.daw.Alive(ctx); err != nil {
		if cur.rp == nil {
			fail(fmt.Errorf("%s is not answering: %w", cur.name, err))
		}
		if *demo {
			fail(fmt.Errorf("simulated demo studio failed to start: %w", err))
		}
		if err := startREAPER(ctx, cur, *send, *listen); err != nil {
			fail(err)
		}
	}
	if cur.rp != nil && !*demo {
		starter(ctx, data)
	} else if d, ok := cur.daw.(*oscmap.DAW); ok && !*demo {
		mappedStarter(ctx, d, data)
	}

	switch cmd {
	case "ask":
		if pb, ok := reg.Get(reg.Roles.Planner); ok && pb.Kind == "meta" && key == "" {
			fail(fmt.Errorf("MODEL_API_KEY is not set"))
		}
		if eb, ok := reg.Get(reg.Roles.Eyes); ok && eb.Kind == "meta" && reg.Roles.Eyes != "" && reg.Roles.Eyes != reg.Roles.Planner && key == "" {
			fail(fmt.Errorf("MODEL_API_KEY is not set"))
		}
		deps.Sink = console{}
		if *turbo {
			// a multi-track request is three subagents plus the session's
			// own sends, reads and final check: the default 16 is not enough
			cfg.MaxSteps = 26
		}
		a := agent.New(deps, cfg)
		if *ref != "" {
			if _, err := a.SetReference(*ref); err != nil {
				fail(err)
			}
		}
		if *photo != "" {
			a.SetPhoto(*photo)
		}
		mode := agent.Auto
		if *preview {
			mode = agent.Preview
		} else if *turbo {
			mode = agent.Turbo
		}
		text, err := a.Run(ctx, strings.Join(flag.Args()[1:], " "), mode)
		if err != nil {
			fail(err)
		}
		fmt.Println("\n" + text)
		fmt.Fprintln(os.Stderr, usage.summary())
	case "serve":
		srv := server.New(func(sink core.Sink) *agent.Agent {
			d := deps
			d.Sink = sink
			return agent.New(d, cfg)
		})
		waking = func(name string) {
			srv.Emit(core.Event{At: time.Now(), Kind: "thought", Data: name + " is waking up; the first answer after a pause takes a few minutes."})
		}
		srv.Images = filepath.Join(data, "images")
		srv.Renders = filepath.Join(data, "renders")
		srv.Refs = filepath.Join(data, "refs")
		daws := &dawSettings{data: data, listen: *listen, send: *send, cur: cur, use: wire,
			planner: func() (core.LLM, string) { return deps.LLM, cfg.Planner }}
		srv.Status = daws.Alive
		srv.DAWs = daws
		srv.Profiles = st.Profiles
		srv.Snapshots = st.Snapshots
		srv.Bridge = reaper.Bridge{}
		srv.Taste = st
		if ms.voice != "" {
			srv.Voice = muse.Voice{Client: llm, Model: ms.voice, Words: srv.Vocabulary}
		}
		srv.Models = &settings{path: filepath.Join(data, "models.json"), reg: reg, apply: func(ctx context.Context, r models.Registry) error {
			next, err := pick(ctx, r, llm, data)
			if err != nil {
				return err
			}
			deps.LLM, deps.Eyes = next.planner, next.eyes
			wire(daws.Current())
			cfg.Planner, cfg.EyesModel, cfg.NoImages = next.plannerModel, next.eyesModel, next.blind
			srv.Voice = nil
			if next.voice != "" {
				srv.Voice = muse.Voice{Client: llm, Model: next.voice, Words: srv.Vocabulary}
			}
			return srv.Rebuild()
		}}
		fmt.Printf("Tonelab Muse on http://%s\n", *addr)
		if err := srv.ListenAndServe(ctx, *addr); err != nil {
			fail(err)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
}

// console prints the agent's events as they happen.
type console struct{}

func (console) Emit(e core.Event) {
	switch e.Kind {
	case "usage", "tool_result", "request":
		return
	case "tool_call":
		m := e.Data.(map[string]any)
		args, _ := json.Marshal(m["args"])
		if len(args) > 160 {
			args = append(args[:160], "…"...)
		}
		fmt.Printf("→ %s %s\n", m["name"], args)
	case "change":
		m := e.Data.(map[string]any)
		fmt.Printf("   %-24v %v → %.3f (DAW: %s, %.3f) %v\n", m["name"], m["from"], m["sent"], m["text"], m["confirmed"], m["status"])
	default:
		b, _ := json.Marshal(e.Data)
		if len(b) > 400 {
			b = append(b[:400], "…"...)
		}
		fmt.Printf("[%s] %s\n", e.Kind, b)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tonelab-muse:", err)
	os.Exit(1)
}

// onboard has Muse write a command map for a new DAW from its docs and
// verify it live, then saves it for -daw.
func onboard(ctx context.Context, llm core.LLM, model, docsPath, out string) {
	b, err := os.ReadFile(docsPath)
	if err != nil {
		fail(err)
	}
	m, checks, err := oscmap.Onboard(ctx, llm, model, string(b), func(round int, m oscmap.Map, cs []oscmap.Check) {
		fmt.Fprintf(os.Stderr, "round %d: %s\n", round, m.Name)
		for _, c := range cs {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(os.Stderr, "  %s %-13s %.160s\n", mark, c.Role, c.Detail)
		}
	})
	if err != nil {
		fail(err)
	}
	if err := oscmap.Save(m, out); err != nil {
		fail(err)
	}
	fmt.Printf("%s: %d checks passed, map saved to %s\n", m.Name, len(checks), out)
}

// waking tells the page a model server is still loading (set by serve).
var waking func(name string)

// chosen is the registry's roles turned into clients.
type chosen struct {
	planner, eyes           core.LLM
	plannerModel, eyesModel string
	blind                   bool   // the planner cannot read images
	voice                   string // speech-to-text model; "" = off
}

// pick builds a client per role. Meta API roles share one client (one key,
// one budget); others get their own, and a local one is started if needed.
func pick(ctx context.Context, reg models.Registry, meta *muse.Client, data string) (chosen, error) {
	var c chosen
	client := func(name string) (core.LLM, models.Backend, error) {
		b, ok := reg.Get(name)
		if !ok {
			return nil, b, fmt.Errorf("no model named %q (see %s)", name, filepath.Join(data, "models.json"))
		}
		if b.Kind == "meta" {
			return meta, b, nil
		}
		if err := models.Serve(ctx, b, filepath.Join(data, "logs")); err != nil {
			return nil, b, err
		}
		c := models.Client(b)
		c.OnUsage = meta.OnUsage
		c.OnWait = func() {
			if waking != nil {
				waking(b.Name)
			}
		}
		return c, b, nil
	}
	if reg.Roles.Planner == "" {
		return c, errors.New("no planner model chosen")
	}
	p, pb, err := client(reg.Roles.Planner)
	if err != nil {
		return c, err
	}
	c.planner, c.plannerModel, c.blind = p, pb.Model, !pb.Caps.Vision
	if m := os.Getenv("MUSE_MODEL"); m != "" && pb.Kind == "meta" {
		c.plannerModel = m // quick switch between Meta models
	}
	if reg.Roles.Eyes != "" && reg.Roles.Eyes != reg.Roles.Planner {
		e, eb, err := client(reg.Roles.Eyes)
		if err != nil {
			return c, err
		}
		c.eyes, c.eyesModel = e, eb.Model
	}
	if b, ok := reg.Get(reg.Roles.Voice); ok && b.Caps.Speech && b.Kind == "meta" {
		c.voice = b.Model
	}
	return c, nil
}

// glimmerFile is the local Muse Glimmer build to serve: the smallest one
// downloaded, since that is the one that fits this Mac.
func glimmerFile(home string) string {
	fs, _ := filepath.Glob(filepath.Join(home, "Models", "glimmer", "Muse-Glimmer-30B-*.gguf"))
	best, size := "", int64(0)
	for _, f := range fs {
		if st, err := os.Stat(f); err == nil && (best == "" || st.Size() < size) {
			best, size = f, st.Size()
		}
	}
	if best == "" {
		return filepath.Join(home, "Models", "glimmer", "Muse-Glimmer-30B-KQuant-17GB-Q4_K_M.gguf")
	}
	return best
}

// settings is the model registry behind the UI's Models panel.
type settings struct {
	path  string
	reg   models.Registry
	apply func(context.Context, models.Registry) error
}

func (s *settings) Get() models.Registry { return s.reg }

func (s *settings) Apply(ctx context.Context, r models.Registry) error {
	if err := s.apply(ctx, r); err != nil {
		return err
	}
	s.reg = r
	return r.Save(s.path)
}

func (s *settings) Probe(ctx context.Context, b models.Backend) models.Caps {
	if err := models.Serve(ctx, b, filepath.Join(filepath.Dir(s.path), "logs")); err != nil {
		return models.Caps{Note: err.Error()}
	}
	return models.Probe(ctx, b)
}
