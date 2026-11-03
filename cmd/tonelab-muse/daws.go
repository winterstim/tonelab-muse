package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/oscmap"
	"github.com/winterstim/tonelab-muse/internal/daw/oscmap/bundled"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/internal/eyes"
	"github.com/winterstim/tonelab-muse/internal/server"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/riff"
)

// studio is one connected DAW and the pieces that depend on which DAW it is.
type studio struct {
	name   string // "reaper" or a saved map's name
	daw    core.DAW
	rp     *reaper.REAPER   // REAPER only: plugin windows, bridge, renders
	render core.Renderer    // listening, where the DAW allows it
	chain  core.ChainEditor // adding and removing plugins, where the DAW allows it
	close  func() error
}

// openStudio connects to REAPER ("" or "reaper") or to a DAW from its saved
// command map: a path, or a name under data/daws.
func openStudio(name, data, listen, send string) (*studio, error) {
	if name == "" || name == "reaper" {
		conn, err := reaper.Dial(listen, send)
		if err != nil {
			return nil, err
		}
		rp := reaper.New(conn)
		b := &reaper.Bridge{}
		rp.Bridge = b
		return &studio{name: "reaper", daw: rp, rp: rp, chain: *b, close: conn.Close,
			render: reaperRender(rp, filepath.Join(data, "renders"))}, nil
	}
	path := name
	if !strings.HasSuffix(path, ".json") {
		path = filepath.Join(data, "daws", name+".json")
	}
	added, err := oscmap.MergeBundled(path, bundled.FS)
	if err != nil {
		return nil, fmt.Errorf("topping up %s from the bundled map: %w", path, err)
	}
	if len(added) > 0 {
		fmt.Fprintf(os.Stderr, "%s: added commands your saved map lacked: %s\n", filepath.Base(path), strings.Join(added, ", "))
	}
	m, err := oscmap.Load(path)
	if err != nil {
		return nil, err
	}
	if m.App == "" {
		m.App = appOnPort(context.Background(), m.Port)
	}
	d, err := oscmap.Open(m)
	if err != nil {
		return nil, err
	}
	s := &studio{name: strings.TrimSuffix(filepath.Base(path), ".json"), daw: d, close: d.Close}
	if m.RenderTrackStart != nil && m.RenderTrackResult != nil {
		s.render = d // Live renders its post-FX track output inside the DAW.
	} else if m.App != "" && len(m.Play.Send) > 0 { // fallback: record the DAW's own output
		s.render = &eyes.AppRenderer{T: d, Bundle: m.App, Dir: filepath.Join(data, "renders")}
	}
	if m.Chains() { // the map can add devices: after the same consent as REAPER's
		s.chain = &oscmap.Chain{D: d, Consent: filepath.Join(data, "bridge", "enabled")}
	}
	return s, nil
}

// reaperRender has REAPER render through its startup script, installed
// here so listening needs no setup; REAPER loads it on its next start.
// Until then a Mac records REAPER's audio instead.
func reaperRender(rp *reaper.REAPER, dir string) core.Renderer {
	rn := &reaper.Renderer{Dir: dir}
	if restart, err := (reaper.Bridge{}).Install(); err != nil {
		fmt.Fprintln(os.Stderr, "listening:", err)
	} else if restart {
		fmt.Fprintln(os.Stderr, "listening: restart REAPER once so it loads the agent's script")
	}
	if runtime.GOOS != "darwin" {
		return rn
	}
	return firstOf{rn, &eyes.AppRenderer{T: rp, Bundle: reaper.Bundle, Dir: dir}}
}

// startREAPER sets REAPER up to talk to the app and opens it: the
// person only closes REAPER, if it is open without remote control.
func startREAPER(ctx context.Context, s *studio, send, listen string) error {
	if reaper.Running() {
		return errors.New("REAPER is open but not listening for the app. Close REAPER and start me again: I switch its remote control (OSC) on myself")
	}
	changed, err := reaper.EnableOSC(send, listen)
	if err != nil {
		return fmt.Errorf("setting REAPER up: %w", err)
	}
	if changed {
		fmt.Fprintln(os.Stderr, "REAPER: switched on its OSC remote control (your old settings are in reaper.ini.before-agent)")
	}
	fmt.Fprintln(os.Stderr, "REAPER: starting it…")
	return reaper.Launch(ctx, s.daw.Alive)
}

// starter gives an empty REAPER project a guitar track to work on, with
// the built-in riff, so a first try needs no recording.
func starter(ctx context.Context, data string) {
	path, err := riffFile(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starter riff:", err)
		return
	}
	b := reaper.Bridge{}
	if _, err := b.Ping(ctx); err != nil {
		return // the script loads on REAPER's next start
	}
	if made, err := b.Starter(ctx, path); err != nil {
		fmt.Fprintln(os.Stderr, "starter track:", err)
	} else if made {
		fmt.Fprintln(os.Stderr, "REAPER: the project was empty, so I added a Guitar track with a riff to try tones on")
	}
}

// riffFile is the built-in riff as a WAV in data, written on first use.
func riffFile(data string) (string, error) {
	path, err := filepath.Abs(filepath.Join(data, "riff.wav"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	os.MkdirAll(data, 0o755)
	return path, riff.WriteWAV(path, riff.Generate(48000, 16), 48000)
}

// mappedStarter does for a mapped DAW what starter does for REAPER: an
// empty set gets the riff on its first audio track. Nothing is loaded into
// a set that already has a clip.
func mappedStarter(ctx context.Context, d *oscmap.DAW, data string) {
	path, err := riffFile(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starter riff:", err)
		return
	}
	idx, why, err := d.Starter(ctx, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "starter track:", err)
	}
	if idx == 0 && why != "" {
		fmt.Fprintf(os.Stderr, "%s: starter skipped: %s\n", d.M.Name, why)
	}
	if idx > 0 {
		fmt.Fprintf(os.Stderr, "%s: the set had no clips, so I put the riff on track %d to try tones on\n", d.M.Name, idx)
	}
}

// firstOf renders with REAPER's script once it answers, else the fallback.
type firstOf struct {
	rn       *reaper.Renderer
	fallback core.Renderer
}

func (f firstOf) Render(ctx context.Context, track int, seconds float64) (string, error) {
	if f.rn.Ready(ctx) {
		return f.rn.Render(ctx, track, seconds)
	}
	return f.fallback.Render(ctx, track, seconds)
}

// dawSettings lets the page switch DAWs and teach the app a new one.
type dawSettings struct {
	mu           sync.Mutex
	data         string
	listen, send string
	cur          *studio
	planner      func() (core.LLM, string) // the registry's planner, now
	use          func(*studio)             // rewires the agent's dependencies
}

func (d *dawSettings) Current() *studio {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur
}

func (d *dawSettings) Alive(ctx context.Context) error { return d.Current().daw.Alive(ctx) }

func (d *dawSettings) List() (string, []server.DAWInfo) {
	out := []server.DAWInfo{{ID: "reaper", Label: reaper.App}}
	fs, _ := filepath.Glob(filepath.Join(d.data, "daws", "*.json"))
	sort.Strings(fs)
	for _, f := range fs {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		label := id
		if m, err := oscmap.Load(f); err == nil && m.Name != "" {
			label = m.Name
		}
		out = append(out, server.DAWInfo{ID: id, Label: label})
	}
	return d.Current().name, out
}

// Use connects to another DAW and closes the old one only once the new one
// answers.
func (d *dawSettings) Use(ctx context.Context, name string) error {
	s, err := openStudio(name, d.data, d.listen, d.send)
	if err != nil {
		return err
	}
	if err := s.daw.Alive(ctx); err != nil {
		s.close()
		return fmt.Errorf("%s is not answering: %w", s.name, err)
	}
	d.mu.Lock()
	old := d.cur
	d.cur = s
	d.mu.Unlock()
	d.use(s)
	old.close()
	return nil
}

// Onboard has Muse read the DAW's docs (pasted text or a link), write a
// command map, verify it against the running DAW and save it.
func (d *dawSettings) Onboard(ctx context.Context, name, docs, url string, progress func(string)) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "reaper" || strings.ContainsAny(name, `/\. `) {
		return errors.New("give the DAW a short name, like ableton")
	}
	if url != "" { // a link and pasted notes may come together
		page, err := fetchDocs(ctx, url)
		if err != nil {
			return err
		}
		docs = page + "\n\n" + docs
	}
	if strings.TrimSpace(docs) == "" {
		return errors.New("paste the DAW's remote-control docs or a link to them")
	}
	llm, model := d.planner()
	// The test needs the DAW's reply port, which the current connection may
	// hold: let go of it, and come back to it if learning fails.
	prev := d.Current()
	if prev.rp == nil {
		prev.close()
	}
	back := func() {
		if prev.rp == nil {
			if err := d.Use(context.Background(), prev.name); err != nil {
				progress("could not reconnect to " + prev.name + ": " + err.Error())
			}
		}
	}
	m, checks, err := oscmap.Onboard(ctx, llm, model, docs, func(round int, m oscmap.Map, cs []oscmap.Check) {
		ok, failed := 0, ""
		for _, c := range cs {
			if c.OK {
				ok++
			} else {
				failed += fmt.Sprintf("; %s failed: %.200s", c.Role, c.Detail)
			}
		}
		progress(fmt.Sprintf("round %d: %d of %d commands work in %s%s", round, ok, len(cs), m.Name, failed))
	})
	if err != nil {
		back()
		return err
	}
	if m.App == "" { // not in the docs: ask macOS which app answers on the port
		m.App = appOnPort(ctx, m.Port)
	}
	if err := oscmap.Save(m, filepath.Join(d.data, "daws", name+".json")); err != nil {
		back()
		return err
	}
	progress(fmt.Sprintf("%s: all %d checks passed", m.Name, len(checks)))
	return nil
}

// fetchDocs reads a docs page; a GitHub repo link becomes its README.
func fetchDocs(ctx context.Context, url string) (string, error) {
	if rest, ok := strings.CutPrefix(url, "https://github.com/"); ok && strings.Count(strings.Trim(rest, "/"), "/") == 1 {
		url = "https://raw.githubusercontent.com/" + strings.Trim(rest, "/") + "/HEAD/README.md"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the docs link answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 400<<10))
	return string(b), err
}

// appOnPort is the bundle id of the app listening on a local UDP port.
func appOnPort(ctx context.Context, port int) string {
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-t", fmt.Sprintf("-iUDP:%d", port)).Output()
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if pid == "" {
		return ""
	}
	info, err := exec.CommandContext(ctx, "lsappinfo", "info", "-only", "bundleid", pid).Output()
	if err != nil {
		return ""
	}
	_, after, ok := strings.Cut(string(info), `bundleID="`)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(after, `"`)
	return id
}

type demoRenderer struct {
	dir string
}

func (r *demoRenderer) Render(_ context.Context, track int, seconds float64) (string, error) {
	if seconds <= 0 {
		seconds = 4
	}
	p := filepath.Join(r.dir, fmt.Sprintf("demo_render_track%d_%d.wav", track, time.Now().UnixNano()))
	samples := riff.Generate(44100, seconds)
	if err := riff.WriteWAV(p, samples, 44100); err != nil {
		return "", err
	}
	return p, nil
}

// openDemoStudio wires reaper.NewFake() and a lightweight synthetic audio renderer
// so tonelab-muse can run standalone without a real DAW installed.
func openDemoStudio(data string) (*studio, error) {
	rp, err := reaper.NewFake()
	if err != nil {
		return nil, fmt.Errorf("starting demo studio: %w", err)
	}
	rendersDir := filepath.Join(data, "renders")
	_ = os.MkdirAll(rendersDir, 0o755)

	rnd := &demoRenderer{dir: rendersDir}
	b := &reaper.Bridge{}
	if rp.Bridge != nil {
		b = rp.Bridge
	}
	return &studio{
		name:   "demo studio (simulated multi-track)",
		daw:    rp,
		rp:     rp,
		chain:  *b,
		render: rnd,
		close:  rp.Close,
	}, nil
}
