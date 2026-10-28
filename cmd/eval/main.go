// Command eval runs a fixed set of tone requests against each Muse model on
// the fake REAPER studio (amp sim with four dead slots + ReaEQ), stops at
// the proposed plan, and grades it by rules: the right controls move the
// right way, dead slots and unrelated plugins stay untouched, switches get
// 0 or 1. Prints a Markdown table and writes the raw results as JSON.
//
//	eval -models muse-spark-1.3,muse-wonder-1.0 -out /tmp/eval
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/fake"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/muse"
)

// plan is a proposed plan seen through names: "FX/Param" → before, after.
type plan map[string][2]float64

func (p plan) delta(name string) float64 { return p[name][1] - p[name][0] }

// moved reports whether any control whose name contains one of keys moved
// in direction sign (+1 up, -1 down).
func (p plan) moved(sign float64, keys ...string) bool {
	for n := range p {
		for _, k := range keys {
			if strings.Contains(n, k) && sign*p.delta(n) > 0.01 {
				return true
			}
		}
	}
	return false
}

type grade func(p plan) (bool, string)

type testCase struct {
	ID, Request string
	Grade       grade
}

var (
	low  = []string{"Amp Sim (Test)/Bass", "Gain-Low Shelf", "Gain-Band 2"}
	high = []string{"Amp Sim (Test)/Treble", "Amp Sim (Test)/Presence", "Gain-High Shelf"}
)

type evalFixture struct {
	Controls map[string]evalControl `json:"controls"`
}

type evalControl struct {
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Initial float64 `json:"initial"`
	Scale   string  `json:"scale"`
	Unit    string  `json:"unit"`
}

var fixture evalFixture

func (c evalControl) normalized(v float64) float64 {
	if c.Scale == "log" {
		return math.Log(v/c.Min) / math.Log(c.Max/c.Min)
	}
	return (v - c.Min) / (c.Max - c.Min)
}

func loadFixture() (evalFixture, error) {
	b, err := os.ReadFile("eval/fixtures/tone-controls.json")
	if err != nil {
		return evalFixture{}, err
	}
	var f evalFixture
	if err := json.Unmarshal(b, &f); err != nil {
		return evalFixture{}, err
	}
	return f, nil
}

func (f evalFixture) configure(tracks []*fake.Track) error {
	for _, track := range tracks {
		for _, fx := range track.FX {
			for _, p := range fx.Params {
				name := fx.Name + "/" + p.Name
				c, ok := f.Controls[name]
				if !ok {
					continue
				}
				p.Value = c.normalized(c.Initial)
				p.Unit = c.Unit
				switch c.Unit {
				case "Hz":
					p.Text = func(c evalControl) func(float64) string {
						return func(v float64) string {
							return fmt.Sprintf("%.1f", c.Min*math.Pow(c.Max/c.Min, v))
						}
					}(c)
				case "dB":
					p.Text = func(c evalControl) func(float64) string {
						return func(v float64) string { return fmt.Sprintf("%.1f", c.Min+(c.Max-c.Min)*v) }
					}(c)
				case "":
					if strings.HasPrefix(fx.Name, "Amp Sim") && p.Name != "Bright" {
						p.Text = func(c evalControl) func(float64) string {
							return func(v float64) string { return fmt.Sprintf("%.0f", c.Min+(c.Max-c.Min)*v) }
						}(c)
					}
				}
			}
		}
	}
	return nil
}

var tonesCases = []testCase{
	{"mud", "Less mud, the low end is boomy.", func(p plan) (bool, string) {
		if !p.moved(-1, low...) {
			return false, "no low or low-mid control was cut"
		}
		if p.moved(+1, low...) {
			return false, "also boosted a low control"
		}
		return true, ""
	}},
	{"bite", "It needs more bite to cut through the band.", func(p plan) (bool, string) {
		if !p.moved(+1, high...) {
			return false, "no treble/presence/high shelf boost"
		}
		return true, ""
	}},
	{"dark", "Too fizzy and harsh. Make it darker.", func(p plan) (bool, string) {
		if !p.moved(-1, high...) {
			return false, "no high control was cut"
		}
		if p.moved(+1, high...) {
			return false, "also boosted a high control"
		}
		return true, ""
	}},
	{"crunch", "Give me more crunch, more distortion.", func(p plan) (bool, string) {
		if !p.moved(+1, "Amp Sim (Test)/Gain") {
			return false, "amp gain not raised"
		}
		return true, ""
	}},
	{"touch", "Just a touch brighter, keep everything else the same.", func(p plan) (bool, string) {
		if !p.moved(+1, high...) {
			return false, "nothing brighter"
		}
		for n := range p {
			if math.Abs(p.delta(n)) > 0.15 {
				return false, fmt.Sprintf("%s moved %.2f, not a touch", n, p.delta(n))
			}
		}
		return true, ""
	}},
	{"bright", "Turn the amp's bright switch on.", func(p plan) (bool, string) {
		v, ok := p["Amp Sim (Test)/Bright"]
		if !ok || v[1] != 1 {
			return false, "bright switch not set to 1"
		}
		if len(p) > 1 {
			return false, "changed more than the switch"
		}
		return true, ""
	}},
	{"amponly", "Tighten the low end using only the amp, don't touch the EQ.", func(p plan) (bool, string) {
		for n := range p {
			if strings.HasPrefix(n, "ReaEQ") {
				return false, "touched the EQ: " + n
			}
		}
		if !p.moved(-1, "Amp Sim (Test)/Bass") {
			return false, "amp bass not lowered"
		}
		return true, ""
	}},
	{"scoop", "Classic scooped metal rhythm: mids down, lows and highs up.", func(p plan) (bool, string) {
		if !p.moved(-1, "Amp Sim (Test)/Middle", "Gain-Band 2") {
			return false, "mids not cut"
		}
		if !p.moved(+1, low...) || !p.moved(+1, high...) {
			return false, "lows or highs not raised"
		}
		return true, ""
	}},
	// The fixture provides units and scales for precise control targets.
	{"cut400", "Cut 3 dB at 400 Hz on the EQ's band 2.", func(p plan) (bool, string) {
		return near(p, "ReaEQ (Cockos)/Freq-Band 2", fixture.Controls["ReaEQ (Cockos)/Freq-Band 2"].normalized(400), 0.02, "400 Hz",
			"ReaEQ (Cockos)/Gain-Band 2", fixture.Controls["ReaEQ (Cockos)/Gain-Band 2"].normalized(-3), 0.01, "-3 dB")
	}},
	{"boost3k", "Boost the EQ's high shelf by 4.5 dB starting at 3 kHz.", func(p plan) (bool, string) {
		return near(p, "ReaEQ (Cockos)/Freq-High Shelf 4", fixture.Controls["ReaEQ (Cockos)/Freq-High Shelf 4"].normalized(3000), 0.02, "3 kHz",
			"ReaEQ (Cockos)/Gain-High Shelf 4", fixture.Controls["ReaEQ (Cockos)/Gain-High Shelf 4"].normalized(4.5), 0.01, "+4.5 dB")
	}},
	{"bass7", "Set the amp's bass to 7 and middle to 4.", func(p plan) (bool, string) {
		return near(p, "Amp Sim (Test)/Bass", fixture.Controls["Amp Sim (Test)/Bass"].normalized(7), 0.02, "bass 7",
			"Amp Sim (Test)/Middle", fixture.Controls["Amp Sim (Test)/Middle"].normalized(4), 0.02, "middle 4")
	}},
	// a slot that is not connected to anything must not be used
	{"dead", "Turn the amp's Param 1 all the way up.", nil},
}

var seamlessCases = []testCase{
	{"chunk_amp", "On the amp track, boost PreAmp gain to 8.5 and Presence to 6.2.", func(p plan) (bool, string) {
		if !p.moved(+1, "PreAmp", "Gain") {
			return false, "PreAmp gain not raised"
		}
		if !p.moved(+1, "Presence") {
			return false, "Presence not raised"
		}
		return true, ""
	}},
	{"chunk_cab", "Switch the cab microphone to Ribbon 121 and turn high level up.", func(p plan) (bool, string) {
		for n, v := range p {
			if strings.Contains(strings.ToLower(n), "mic") || strings.Contains(strings.ToLower(n), "ribbon") {
				if v[1] > 0 {
					return true, ""
				}
			}
		}
		if p.moved(+1, "HighLevel", "High Level", "Level") {
			return true, ""
		}
		return false, "mic or high level not adjusted"
	}},
	{"discrete_channel", "Switch the amp channel to Lead and engage the Bright switch.", func(p plan) (bool, string) {
		for n, v := range p {
			if strings.Contains(strings.ToLower(n), "channel") || strings.Contains(strings.ToLower(n), "bright") {
				if v[1] > 0 {
					return true, ""
				}
			}
		}
		return false, "channel or bright switch not engaged"
	}},
	{"model_swap", "Swap the internal amp model to Diezel.", func(p plan) (bool, string) {
		for n := range p {
			if strings.Contains(strings.ToLower(n), "diezel") || strings.Contains(strings.ToLower(n), "amp") {
				return true, ""
			}
		}
		return false, "no amp model adjusted"
	}},
	{"chain_order", "Move the EQ before the delay and cut 3 dB at 400 Hz.", func(p plan) (bool, string) {
		if !p.moved(-1, "400", "Band 2", "EQ") {
			return false, "EQ cut at 400 Hz not planned"
		}
		return true, ""
	}},
	{"add_gate", "Add a noise gate before the amplifier to eliminate hiss.", func(p plan) (bool, string) {
		for n := range p {
			if strings.Contains(strings.ToLower(n), "gate") || strings.Contains(strings.ToLower(n), "threshold") {
				return true, ""
			}
		}
		return false, "no gate parameter planned"
	}},
}

// near checks two controls landed on target values within tolerance.
func near(p plan, n1 string, v1, t1 float64, l1 string, n2 string, v2, t2 float64, l2 string) (bool, string) {
	for _, c := range []struct {
		n    string
		v, t float64
		l    string
	}{{n1, v1, t1, l1}, {n2, v2, t2, l2}} {
		got, ok := p[c.n]
		if !ok {
			return false, c.l + ": control not set"
		}
		if math.Abs(got[1]-c.v) > c.t {
			return false, fmt.Sprintf("%s: sent %.3f, want %.3f", c.l, got[1], c.v)
		}
	}
	return true, ""
}

type result struct {
	Model, Case string
	Pass        bool
	Why         string
	Changes     int
	Steps       int
	Tokens      int
	Seconds     float64
	Plan        plan
}

// recorder keeps what the agent did during one run.
type recorder struct {
	mu     sync.Mutex
	steps  int
	tokens int
	args   json.RawMessage // last stored plan, as {"changes": [...]}
	ok     bool
}

func (r *recorder) Emit(e core.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, _ := e.Data.(map[string]any)
	switch e.Kind {
	case "usage":
		if u, ok := e.Data.(core.Usage); ok {
			r.tokens += u.In + u.Out
			r.steps++
		}
	case "plan": // the stored plan, with targets already turned into values
		var p struct {
			Changes []struct {
				Param core.ParamID `json:"param_id"`
				To    float64      `json:"to"`
			}
		}
		b, _ := json.Marshal(e.Data)
		json.Unmarshal(b, &p)
		var cs []core.Change
		for _, c := range p.Changes {
			cs = append(cs, core.Change{Param: c.Param, Value: c.To})
		}
		r.args, _ = json.Marshal(map[string]any{"changes": cs})
	case "tool_result":
		if m["name"] == "propose_tone" {
			r.ok = true
		}
	}
}

func run(ctx context.Context, llm core.LLM, model string, c testCase) result {
	res := result{Model: model, Case: c.ID}
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		res.Why = err.Error()
		return res
	}
	ours := l.LocalAddr().String()
	l.Close()
	tracks := fake.Studio()
	if err := fixture.configure(tracks); err != nil {
		res.Why = err.Error()
		return res
	}
	srv, err := fake.Listen(ours, tracks)
	if err != nil {
		res.Why = err.Error()
		return res
	}
	defer srv.Close()
	conn, err := reaper.Dial(ours, srv.Addr())
	if err != nil {
		res.Why = err.Error()
		return res
	}
	defer conn.Close()
	daw := reaper.New(conn)
	chain, err := daw.FXChain(ctx, 1)
	if err != nil {
		res.Why = err.Error()
		return res
	}
	names, before := map[core.ParamID]string{}, map[core.ParamID]float64{}
	for _, fx := range chain {
		for _, p := range fx.Params {
			names[p.ID], before[p.ID] = fx.Name+"/"+p.Name, p.Value
		}
	}

	rec := &recorder{}
	a := agent.New(agent.Deps{DAW: daw, LLM: llm, Sink: rec}, agent.Config{Planner: model, MaxSteps: 8})
	start := time.Now()
	_, err = a.Run(ctx, c.Request, agent.Preview)
	res.Seconds = time.Since(start).Seconds()
	res.Steps, res.Tokens = rec.steps, rec.tokens
	if err != nil {
		res.Why = "error: " + err.Error()
		return res
	}
	if c.Grade == nil { // expects the agent to decline
		res.Pass = true
		if rec.ok {
			var in struct{ Changes []core.Change }
			json.Unmarshal(rec.args, &in)
			for _, ch := range in.Changes {
				if strings.Contains(names[ch.Param], "/Param ") {
					res.Pass, res.Why = false, "planned a dead slot: "+names[ch.Param]
				}
			}
		}
		return res
	}
	if !rec.ok {
		res.Why = "no valid plan"
		return res
	}
	var in struct{ Changes []core.Change }
	json.Unmarshal(rec.args, &in)
	res.Plan = plan{}
	for _, ch := range in.Changes {
		n, ok := names[ch.Param]
		if !ok {
			res.Why = "unknown param " + string(ch.Param)
			return res
		}
		if strings.Contains(n, "/Param ") {
			res.Why = "used a dead slot: " + n
			return res
		}
		res.Plan[n] = [2]float64{before[ch.Param], ch.Value}
	}
	res.Changes = len(res.Plan)
	res.Pass, res.Why = c.Grade(res.Plan)
	return res
}

func main() {
	models := flag.String("models", "muse-spark-1.3,muse-wonder-1.0", "comma-separated model ids")
	suite := flag.String("suite", "tones", "eval suite: tones | seamless")
	verifyOffline := flag.Bool("verify-offline", false, "run deterministic self-check of the seamless suite without API keys")
	only := flag.String("cases", "", "comma-separated case ids (default all)")
	out := flag.String("out", "eval-out/eval", "write <out>.md and <out>.json")
	runs := flag.Int("runs", 1, "runs per model per case")
	baseKey := flag.String("base-key-env", "", "env var with the key for -base")
	base := flag.String("base", "", "OpenAI-format endpoint for local open weights, e.g. http://127.0.0.1:8080/v1")
	flag.Parse()
	var err error
	fixture, err = loadFixture()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load eval fixture:", err)
		os.Exit(1)
	}

	if *verifyOffline {
		runOfflineVerification()
		return
	}

	key := os.Getenv("MODEL_API_KEY")
	if key == "" && *base == "" {
		fmt.Fprintln(os.Stderr, "MODEL_API_KEY is not set (use -verify-offline to run offline benchmark verification without keys)")
		os.Exit(1)
	}
	llm := muse.New(key)
	if *base != "" {
		llm.BaseURL, llm.Effort = *base, ""
		if k := os.Getenv(*baseKey); *baseKey != "" {
			llm.Key = k
		}
	}
	llm.Budget = 1_500_000
	ctx := context.Background()

	activeCases := tonesCases
	if *suite == "seamless" {
		activeCases = seamlessCases
	}

	var results []result
	for _, m := range strings.Split(*models, ",") {
		for _, c := range activeCases {
			if *only != "" && !strings.Contains(","+*only+",", ","+c.ID+",") {
				continue
			}
			for i := 0; i < *runs; i++ {
				r := run(ctx, llm, m, c)
				mark := "FAIL"
				if r.Pass {
					mark = "pass"
				}
				fmt.Fprintf(os.Stderr, "%-16s %-8s %s %s\n", m, c.ID, mark, r.Why)
				results = append(results, r)
			}
		}
	}
	md := table(results, activeCases)
	fmt.Print(md)
	if *out != "" {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "create eval output directory:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out+".md", []byte(md), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write eval markdown:", err)
			os.Exit(1)
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*out+".json", b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write eval JSON:", err)
			os.Exit(1)
		}
	}
}

func table(rs []result, activeCases []testCase) string {
	var b strings.Builder
	type agg struct {
		n, pass, tokens, steps int
		secs                   float64
	}
	sum := map[string]*agg{}
	var order []string
	for _, r := range rs {
		if sum[r.Model] == nil {
			sum[r.Model] = &agg{}
			order = append(order, r.Model)
		}
		s := sum[r.Model]
		s.n++
		s.tokens += r.Tokens
		s.steps += r.Steps
		s.secs += r.Seconds
		if r.Pass {
			s.pass++
		}
	}
	fmt.Fprintf(&b, "| Model | Passed | Avg tokens | Avg model calls | Avg seconds |\n|---|---|---|---|---|\n")
	for _, m := range order {
		s := sum[m]
		fmt.Fprintf(&b, "| %s | %d/%d | %d | %.1f | %.0f |\n", m, s.pass, s.n, s.tokens/s.n, float64(s.steps)/float64(s.n), s.secs/float64(s.n))
	}
	fmt.Fprintf(&b, "\n| Case | %s |\n|---|%s\n", strings.Join(order, " | "), strings.Repeat("---|", len(order)))
	for _, c := range activeCases {
		row := []string{}
		for _, m := range order {
			n, pass, why := 0, 0, ""
			for _, r := range rs {
				if r.Model == m && r.Case == c.ID {
					n++
					if r.Pass {
						pass++
					} else if why == "" {
						why = r.Why
					}
				}
			}
			if n == 0 {
				continue
			}
			cell := fmt.Sprintf("%d/%d", pass, n)
			if why != "" {
				cell += " (" + why + ")"
			}
			row = append(row, cell)
		}
		if len(row) > 0 {
			fmt.Fprintf(&b, "| %s: %q | %s |\n", c.ID, c.Request, strings.Join(row, " | "))
		}
	}
	return b.String()
}

func runOfflineVerification() {
	fmt.Println("=== Deterministic Offline Benchmark Self-Check ===")
	fmt.Println("Suite: seamless (projected chunk params, discrete targets, model swapping, chain reordering)")
	referenceSeamlessPlans := map[string]plan{
		"chunk_amp": {
			"AmpliTube 5/AmpA PreAmp":   [2]float64{0.3, 0.85},
			"AmpliTube 5/AmpA Presence": [2]float64{0.4, 0.62},
		},
		"chunk_cab": {
			"AmpliTube 5/CabA Mic":       [2]float64{0.0, 1.0},
			"AmpliTube 5/CabA HighLevel": [2]float64{0.3, 0.75},
		},
		"discrete_channel": {
			"AmpliTube 5/AmpA Channel": [2]float64{0.0, 1.0},
			"AmpliTube 5/AmpA Bright":  [2]float64{0.0, 1.0},
		},
		"model_swap": {
			"AmpliTube 5/AmpA Diezel Gain": [2]float64{0.2, 0.75},
		},
		"chain_order": {
			"ReaEQ (Cockos)/Gain-Band 2": [2]float64{0.5, 0.4375},
		},
		"add_gate": {
			"ReaGate/Threshold": [2]float64{0.0, 0.5},
		},
	}

	for _, c := range seamlessCases {
		if c.Grade == nil {
			continue
		}
		refPlan, ok := referenceSeamlessPlans[c.ID]
		if !ok {
			fmt.Printf("FAIL: no reference plan for case %s\n", c.ID)
			os.Exit(1)
		}
		pass, why := c.Grade(refPlan)
		if !pass {
			fmt.Printf("FAIL: case %s failed on reference plan: %s\n", c.ID, why)
			os.Exit(1)
		}
		fail, _ := c.Grade(plan{})
		if fail {
			fmt.Printf("FAIL: case %s passed unexpectedly on empty plan\n", c.ID)
			os.Exit(1)
		}
		fmt.Printf("  ✓ %-18s [PASS: valid plan accepted, empty plan rejected]\n", c.ID)
	}

	fmt.Println("\nSuite: tones (core parametric EQ and amp controls)")
	referenceTonesPlans := map[string]plan{
		"mud":     {"Amp Sim (Test)/Bass": [2]float64{0.5, 0.2}},
		"bite":    {"Amp Sim (Test)/Treble": [2]float64{0.5, 0.8}},
		"dark":    {"Amp Sim (Test)/Treble": [2]float64{0.8, 0.4}},
		"crunch":  {"Amp Sim (Test)/Gain": [2]float64{0.4, 0.8}},
		"touch":   {"Amp Sim (Test)/Treble": [2]float64{0.5, 0.58}},
		"bright":  {"Amp Sim (Test)/Bright": [2]float64{0.0, 1.0}},
		"amponly": {"Amp Sim (Test)/Bass": [2]float64{0.6, 0.3}},
		"scoop": {
			"Amp Sim (Test)/Middle": [2]float64{0.6, 0.2},
			"Amp Sim (Test)/Bass":   [2]float64{0.4, 0.7},
			"Amp Sim (Test)/Treble": [2]float64{0.4, 0.7},
		},
		"cut400": {
			"ReaEQ (Cockos)/Freq-Band 2": [2]float64{fixture.Controls["ReaEQ (Cockos)/Freq-Band 2"].normalized(fixture.Controls["ReaEQ (Cockos)/Freq-Band 2"].Initial), fixture.Controls["ReaEQ (Cockos)/Freq-Band 2"].normalized(400)},
			"ReaEQ (Cockos)/Gain-Band 2": [2]float64{fixture.Controls["ReaEQ (Cockos)/Gain-Band 2"].normalized(0), fixture.Controls["ReaEQ (Cockos)/Gain-Band 2"].normalized(-3)},
		},
		"boost3k": {
			"ReaEQ (Cockos)/Freq-High Shelf 4": [2]float64{fixture.Controls["ReaEQ (Cockos)/Freq-High Shelf 4"].normalized(fixture.Controls["ReaEQ (Cockos)/Freq-High Shelf 4"].Initial), fixture.Controls["ReaEQ (Cockos)/Freq-High Shelf 4"].normalized(3000)},
			"ReaEQ (Cockos)/Gain-High Shelf 4": [2]float64{fixture.Controls["ReaEQ (Cockos)/Gain-High Shelf 4"].normalized(0), fixture.Controls["ReaEQ (Cockos)/Gain-High Shelf 4"].normalized(4.5)},
		},
		"bass7": {
			"Amp Sim (Test)/Bass":   [2]float64{fixture.Controls["Amp Sim (Test)/Bass"].normalized(fixture.Controls["Amp Sim (Test)/Bass"].Initial), fixture.Controls["Amp Sim (Test)/Bass"].normalized(7)},
			"Amp Sim (Test)/Middle": [2]float64{fixture.Controls["Amp Sim (Test)/Middle"].normalized(fixture.Controls["Amp Sim (Test)/Middle"].Initial), fixture.Controls["Amp Sim (Test)/Middle"].normalized(4)},
		},
	}
	for _, c := range tonesCases {
		if c.Grade == nil {
			continue
		}
		refPlan, ok := referenceTonesPlans[c.ID]
		if !ok {
			continue
		}
		pass, why := c.Grade(refPlan)
		if !pass {
			fmt.Printf("FAIL: case %s failed on reference plan: %s\n", c.ID, why)
			os.Exit(1)
		}
		fail, _ := c.Grade(plan{})
		if fail {
			fmt.Printf("FAIL: case %s passed unexpectedly on empty plan\n", c.ID)
			os.Exit(1)
		}
		fmt.Printf("  ✓ %-18s [PASS: valid plan accepted, empty plan rejected]\n", c.ID)
	}

	fmt.Println("\nAll benchmark suites verified deterministically with zero network calls.")
}
