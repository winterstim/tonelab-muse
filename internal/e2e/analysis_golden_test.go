package e2e

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/ears"
	"github.com/winterstim/tonelab-muse/pkg/riff"
)

// biquad implements standard audio EQ filtering (Robert Bristow-Johnson Audio EQ Cookbook).
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func newPeakingEQ(rate, freq, q, gainDB float64) *biquad {
	a := math.Pow(10, gainDB/40)
	w0 := 2 * math.Pi * freq / rate
	alpha := math.Sin(w0) / (2 * q)
	cosw := math.Cos(w0)

	b0 := 1 + alpha*a
	b1 := -2 * cosw
	b2 := 1 - alpha*a
	a0 := 1 + alpha/a
	a1 := -2 * cosw
	a2 := 1 - alpha/a

	return &biquad{
		b0: b0 / a0,
		b1: b1 / a0,
		b2: b2 / a0,
		a1: a1 / a0,
		a2: a2 / a0,
	}
}

func (bq *biquad) process(in []float64) []float64 {
	out := make([]float64, len(in))
	x1, x2, y1, y2 := bq.x1, bq.x2, bq.y1, bq.y2
	b0, b1, b2, a1, a2 := bq.b0, bq.b1, bq.b2, bq.a1, bq.a2
	for i, x := range in {
		y := b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2
		x2 = x1
		x1 = x
		y2 = y1
		y1 = y
		out[i] = y
	}
	bq.x1, bq.x2, bq.y1, bq.y2 = x1, x2, y1, y2
	return out
}

func normalizedToDB(v float64) float64 {
	return (v - 0.5) * 40.0
}

func dbToNormalized(db float64) float64 {
	return math.Max(0, math.Min(1, db/40.0+0.5))
}

// simulatedEQDAW is a simulated DAW and audio processor that applies real
// parametric filtering and gain adjustments to audio clips.
type simulatedEQDAW struct {
	trackAudio  ears.Audio
	backingWAV  string
	tempDir     string
	renderCount int

	mute float64
	solo float64

	// EQ filter gains in dB
	lowGain      float64
	mudGain      float64
	midGain      float64
	upperGain    float64
	presenceGain float64
	airGain      float64

	// Output level offset in dB
	outputGain float64

	failSoloRender bool
}

func newSimulatedEQDAW(t *testing.T, trackWAV, backingWAV string) *simulatedEQDAW {
	t.Helper()
	a, err := ears.ReadWAV(trackWAV)
	if err != nil {
		t.Fatalf("read track wav %s: %v", trackWAV, err)
	}
	return &simulatedEQDAW{
		trackAudio: a,
		backingWAV: backingWAV,
		tempDir:    t.TempDir(),
	}
}

func (d *simulatedEQDAW) Tracks(context.Context) ([]core.Track, error) {
	return []core.Track{
		{Index: 1, Name: "Guitar", Audio: true},
		{Index: 2, Name: "Backing", Audio: true},
	}, nil
}

func (d *simulatedEQDAW) FXChain(context.Context, int) ([]core.FX, error) {
	return []core.FX{
		{
			Index: 1,
			Name:  "Parametric EQ",
			Params: []core.Param{
				{ID: "fx1.p1", Name: "Low Gain", Value: dbToNormalized(d.lowGain)},
				{ID: "fx1.p2", Name: "Mud Gain", Value: dbToNormalized(d.mudGain)},
				{ID: "fx1.p3", Name: "Presence Gain", Value: dbToNormalized(d.presenceGain)},
				{ID: "fx1.p4", Name: "Air Gain", Value: dbToNormalized(d.airGain)},
				{ID: "fx1.p5", Name: "Output Level", Value: dbToNormalized(d.outputGain)},
			},
		},
	}, nil
}

func (d *simulatedEQDAW) SetParam(_ context.Context, _ int, id core.ParamID, v float64) (core.Applied, error) {
	switch id {
	case "fx1.p1":
		d.lowGain = normalizedToDB(v)
	case "fx1.p2":
		d.mudGain = normalizedToDB(v)
	case "fx1.p3":
		d.presenceGain = normalizedToDB(v)
	case "fx1.p4":
		d.airGain = normalizedToDB(v)
	case "fx1.p5":
		d.outputGain = normalizedToDB(v)
	}
	return core.Applied{Param: id, Sent: v, Confirmed: v, Status: core.StatusConfirmed}, nil
}

func (d *simulatedEQDAW) SetTrack(_ context.Context, _ int, prop string, v float64) (float64, error) {
	switch prop {
	case "mute":
		d.mute = v
	case "solo":
		d.solo = v
	}
	return v, nil
}

func (d *simulatedEQDAW) Undo(context.Context) error  { return nil }
func (d *simulatedEQDAW) Alive(context.Context) error { return nil }

func (d *simulatedEQDAW) Render(_ context.Context, _ int, _ float64) (string, error) {
	d.renderCount++

	// When track is muted during mix checking, render the rest of the mix.
	if d.mute == 1 && d.backingWAV != "" {
		return d.backingWAV, nil
	}

	if d.solo == 1 && d.failSoloRender {
		return "", errors.New("simulated solo render failure")
	}

	// Apply parametric filtering and output gain to the track audio
	rate := float64(d.trackAudio.Rate)
	samples := make([]float64, len(d.trackAudio.Samples))
	copy(samples, d.trackAudio.Samples)

	if d.lowGain != 0 {
		samples = newPeakingEQ(rate, 100, 1.0, d.lowGain).process(samples)
	}
	if d.mudGain != 0 {
		samples = newPeakingEQ(rate, 250, 1.0, d.mudGain).process(samples)
	}
	if d.midGain != 0 {
		samples = newPeakingEQ(rate, 700, 1.0, d.midGain).process(samples)
	}
	if d.upperGain != 0 {
		samples = newPeakingEQ(rate, 2000, 1.0, d.upperGain).process(samples)
	}
	if d.presenceGain != 0 {
		samples = newPeakingEQ(rate, 4500, 1.0, d.presenceGain).process(samples)
	}
	if d.airGain != 0 {
		samples = newPeakingEQ(rate, 8000, 1.0, d.airGain).process(samples)
	}

	// Apply master / output gain
	if d.outputGain != 0 {
		scale := math.Pow(10, d.outputGain/20)
		for i := range samples {
			samples[i] *= scale
		}
	}

	outPath := filepath.Join(d.tempDir, fmt.Sprintf("render_%d.wav", d.renderCount))
	if err := riff.WriteWAV(outPath, samples, d.trackAudio.Rate); err != nil {
		return "", err
	}
	return outPath, nil
}

type testVisualizer struct{ dir string }

func (v testVisualizer) Spectrogram([]string, []string) (string, error) {
	p := filepath.Join(v.dir, "spec.png")
	w, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer w.Close()
	return p, png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
}

// A2: Loudness matching against start.
// Test that level change vs start is tracked across multiple rounds with
// pkg/ears Meter and restored within 1 dB tolerance.
func TestGoldenA2_LoudnessMatchingAgainstStart(t *testing.T) {
	cleanWAV := "../../pkg/ears/testdata/clean.wav"
	daw := newSimulatedEQDAW(t, cleanWAV, "")
	vis := testVisualizer{daw.tempDir}
	meter := ears.Meter{}

	a := agent.New(agent.Deps{
		DAW:        daw,
		Renderer:   daw,
		Measurer:   meter,
		Visualizer: vis,
	}, agent.Config{})

	// Round 0: Listen to baseline sound before any request changes
	out0, err := a.Listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("baseline listen: %v", err)
	}
	m0 := out0.(map[string]any)
	baseMeasure := m0["measured"].(core.Measure)

	// Round 1: Tone change introduces +4.5 dB level boost.
	daw.outputGain = 4.5
	out1, err := a.Listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 1 listen: %v", err)
	}
	m1 := out1.(map[string]any)
	levelVsStart1, ok := m1["level_vs_start_db"].(float64)
	if !ok {
		t.Fatalf("round 1 missing level_vs_start_db: %v", m1)
	}
	if math.Abs(levelVsStart1-4.5) > 0.3 {
		t.Errorf("round 1 level_vs_start_db = %.2f dB, want ~4.5 dB", levelVsStart1)
	}
	if m1["level_note"] == nil {
		t.Errorf("round 1 expected level_note for drift > 1.5 dB, got nil")
	}

	// Round 2: Compensate partially by dialing output down by 3.0 dB (+1.5 dB net vs start).
	daw.outputGain = 1.5
	out2, err := a.Listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 2 listen: %v", err)
	}
	m2 := out2.(map[string]any)
	change2 := m2["level_change_db"].(float64)
	levelVsStart2 := m2["level_vs_start_db"].(float64)
	if math.Abs(change2-(-3.0)) > 0.3 {
		t.Errorf("round 2 level_change_db = %.2f dB, want ~ -3.0 dB", change2)
	}
	if math.Abs(levelVsStart2-1.5) > 0.3 {
		t.Errorf("round 2 level_vs_start_db = %.2f dB, want ~1.5 dB", levelVsStart2)
	}

	// Round 3: Fine compensation dials output down by another 1.2 dB (+0.3 dB net vs start).
	daw.outputGain = 0.3
	out3, err := a.Listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 3 listen: %v", err)
	}
	m3 := out3.(map[string]any)
	change3 := m3["level_change_db"].(float64)
	levelVsStart3 := m3["level_vs_start_db"].(float64)
	if math.Abs(change3-(-1.2)) > 0.3 {
		t.Errorf("round 3 level_change_db = %.2f dB, want ~ -1.2 dB", change3)
	}
	if math.Abs(levelVsStart3-0.3) > 0.3 {
		t.Errorf("round 3 level_vs_start_db = %.2f dB, want ~0.3 dB", levelVsStart3)
	}

	// Verify restored within 1 dB tolerance of start level
	if math.Abs(levelVsStart3) > 1.0 {
		t.Fatalf("final level drift %.2f dB exceeds 1.0 dB tolerance", levelVsStart3)
	}
	if m3["level_note"] != nil {
		t.Errorf("round 3 should not have level_note when drift <= 1.5 dB, got %v", m3["level_note"])
	}

	// Verify the final measured WAV is within 1 dB of the baseline measurement
	finalMeasured := m3["measured"].(core.Measure)
	measuredDrift := finalMeasured.Level - baseMeasure.Level
	if math.Abs(measuredDrift) > 1.0 {
		t.Fatalf("measured level drift %.2f dB exceeds 1.0 dB tolerance", measuredDrift)
	}
}

// A4: Mix clash detection (check_mix).
// Using two real audio clips (mudd.wav vs clean.wav), test that ears.Clashes /
// a.CheckMix correctly detects 1/3-octave band masking clashes, reports top
// clashes in order of severity, and cleanly restores mute/solo state.
func TestGoldenA4_MixClashDetection(t *testing.T) {
	muddWAV := "../../pkg/ears/testdata/mudd.wav"
	cleanWAV := "../../pkg/ears/testdata/clean.wav"
	meter := ears.Meter{}

	// Step 1: Direct test of ears.Clashes on real audio clips
	clashes, err := meter.Clashes(muddWAV, cleanWAV)
	if err != nil {
		t.Fatalf("ears.Clashes error: %v", err)
	}
	if len(clashes) == 0 {
		t.Fatalf("expected masking clashes between mudd.wav and clean.wav, got 0")
	}

	// Verify 1/3-octave band structure and severity ordering
	step := math.Pow(2, 1.0/3)
	for i, c := range clashes {
		ratio := c.Hi / c.Lo
		if math.Abs(ratio-step) > 0.05 {
			t.Errorf("clash [%d] band ratio %.3f is not ~1/3-octave (%.3f)", i, ratio, step)
		}
		center := math.Round(math.Sqrt(c.Lo * c.Hi))
		if math.Abs(c.Center-center) > 1 {
			t.Errorf("clash [%d] center %.1f != sqrt(Lo*Hi) %.1f", i, c.Center, center)
		}
		if c.Score <= 0 {
			t.Errorf("clash [%d] score = %.2f; want > 0", i, c.Score)
		}
		if i > 0 && c.Score > clashes[i-1].Score {
			t.Errorf("clashes not sorted in descending order of severity: [%d] %.1f > [%d] %.1f",
				i, c.Score, i-1, clashes[i-1].Score)
		}
	}

	// Step 2: Test a.CheckMix through the agent with simulated DAW
	daw := newSimulatedEQDAW(t, muddWAV, cleanWAV)
	vis := testVisualizer{daw.tempDir}
	a := agent.New(agent.Deps{
		DAW:        daw,
		Renderer:   daw,
		Measurer:   meter,
		Visualizer: vis,
		Mix:        meter,
	}, agent.Config{})

	// Ensure DAW begins with mute = 0, solo = 0
	if daw.mute != 0 || daw.solo != 0 {
		t.Fatalf("initial state not zero: mute=%.0f solo=%.0f", daw.mute, daw.solo)
	}

	out, err := a.CheckMix(context.Background(), 1)
	if err != nil {
		t.Fatalf("CheckMix error: %v", err)
	}
	resMap := out.(map[string]any)
	agentClashes := resMap["clashes"].([]core.Clash)
	if len(agentClashes) != len(clashes) {
		t.Fatalf("CheckMix clashes count %d != direct clashes count %d", len(agentClashes), len(clashes))
	}
	for i := range agentClashes {
		if agentClashes[i].Center != clashes[i].Center || agentClashes[i].Score != clashes[i].Score {
			t.Errorf("clash mismatch at [%d]: got %+v, want %+v", i, agentClashes[i], clashes[i])
		}
	}

	// Verify mute and solo states are cleanly restored to 0
	if daw.mute != 0 || daw.solo != 0 {
		t.Fatalf("CheckMix failed to restore mute/solo state: mute=%.0f solo=%.0f", daw.mute, daw.solo)
	}

	// Step 3: Test clean restoration even when rendering fails during solo
	daw.failSoloRender = true
	_, err = a.CheckMix(context.Background(), 1)
	if err == nil {
		t.Fatalf("expected CheckMix to return error on failed render, got nil")
	}
	if daw.mute != 0 || daw.solo != 0 {
		t.Fatalf("CheckMix failed to restore mute/solo state after error: mute=%.0f solo=%.0f",
			daw.mute, daw.solo)
	}
}

// A5: Reference-gap closing across rounds.
// Create a simulated EQ processor / fake DAW that filters audio. Run a
// multi-round test showing the dB gap across bands between mudd.wav and
// distortion.wav closing progressively over 3 rounds with measurable
// convergence.
func TestGoldenA5_ReferenceGapClosingAcrossRounds(t *testing.T) {
	muddWAV := "../../pkg/ears/testdata/mudd.wav"
	distortionWAV := "../../pkg/ears/testdata/distortion.wav"

	daw := newSimulatedEQDAW(t, muddWAV, "")
	vis := testVisualizer{daw.tempDir}
	meter := ears.Meter{}

	a := agent.New(agent.Deps{
		DAW:        daw,
		Renderer:   daw,
		Measurer:   meter,
		Visualizer: vis,
	}, agent.Config{})

	if _, err := a.SetReference(distortionWAV); err != nil {
		t.Fatalf("SetReference error: %v", err)
	}

	// Round 1: Initial state (flat EQ, uncorrected mudd.wav)
	res1, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 1 MatchReference: %v", err)
	}
	m1 := res1.(map[string]any)
	dist1 := m1["distance_db"].(float64)
	gap1 := m1["gap_db"].(map[string]float64)

	// Mud is excessive (negative gap means reference has much less mud)
	// Presence and Air are lacking (positive gap means reference has much more)
	if gap1["mud"] >= 0 || gap1["presence"] <= 0 || gap1["air"] <= 0 {
		t.Fatalf("unexpected initial gaps: mud=%.1f presence=%.1f air=%.1f",
			gap1["mud"], gap1["presence"], gap1["air"])
	}
	if dist1 < 8.0 {
		t.Fatalf("initial distance %.2f dB unexpectedly low for mudd vs distortion", dist1)
	}

	// Round 2: Attenuate low and mud frequencies
	daw.lowGain = -8.0
	daw.mudGain = -8.0
	res2, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 2 MatchReference: %v", err)
	}
	m2 := res2.(map[string]any)
	dist2 := m2["distance_db"].(float64)
	gap2 := m2["gap_db"].(map[string]float64)

	if dist2 >= dist1 {
		t.Fatalf("round 2 distance %.2f dB did not improve on round 1 %.2f dB", dist2, dist1)
	}
	// Mud gap should close measurably
	if math.Abs(gap2["mud"]) >= math.Abs(gap1["mud"]) {
		t.Errorf("mud gap did not close: |%.1f| >= |%.1f|", gap2["mud"], gap1["mud"])
	}

	// Round 3: Boost presence and upper-mid frequencies
	daw.upperGain = 5.0
	daw.presenceGain = 8.0
	res3, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 3 MatchReference: %v", err)
	}
	m3 := res3.(map[string]any)
	dist3 := m3["distance_db"].(float64)
	gap3 := m3["gap_db"].(map[string]float64)

	if dist3 >= dist2 {
		t.Fatalf("round 3 distance %.2f dB did not improve on round 2 %.2f dB", dist3, dist2)
	}
	// Presence gap should close measurably
	if math.Abs(gap3["presence"]) >= math.Abs(gap1["presence"]) {
		t.Errorf("presence gap did not close: |%.1f| >= |%.1f|", gap3["presence"], gap1["presence"])
	}

	// Round 4: Fine tuning - boost air frequencies
	daw.airGain = 10.0
	res4, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 4 MatchReference: %v", err)
	}
	m4 := res4.(map[string]any)
	dist4 := m4["distance_db"].(float64)

	if dist4 >= dist3 {
		t.Fatalf("round 4 distance %.2f dB did not improve on round 3 %.2f dB", dist4, dist3)
	}

	// Verify convergence history recorded in the agent
	conv := m4["convergence"].([]map[string]any)
	if len(conv) != 4 {
		t.Fatalf("expected 4 convergence rounds recorded, got %d", len(conv))
	}

	// Verify monotonic convergence across all rounds
	for i := 1; i < len(conv); i++ {
		prevDist := conv[i-1]["distance_db"].(float64)
		currDist := conv[i]["distance_db"].(float64)
		if currDist >= prevDist {
			t.Errorf("round %d distance %.2f dB >= round %d distance %.2f dB",
				i+1, currDist, i, prevDist)
		}
	}

	// Verify significant overall convergence: distance closed from ~9.9 dB to < 2.5 dB
	totalReduction := dist1 - dist4
	if totalReduction < 5.0 {
		t.Errorf("total distance reduction %.2f dB is less than 5 dB", totalReduction)
	}
}

// TestGoldenA5_CleanVsCrunchConvergence also verifies reference gap closing
// between clean.wav and crunch.wav across rounds.
func TestGoldenA5_CleanVsCrunchConvergence(t *testing.T) {
	cleanWAV := "../../pkg/ears/testdata/clean.wav"
	crunchWAV := "../../pkg/ears/testdata/crunch.wav"

	daw := newSimulatedEQDAW(t, cleanWAV, "")
	vis := testVisualizer{daw.tempDir}
	meter := ears.Meter{}

	a := agent.New(agent.Deps{
		DAW:        daw,
		Renderer:   daw,
		Measurer:   meter,
		Visualizer: vis,
	}, agent.Config{})

	if _, err := a.SetReference(crunchWAV); err != nil {
		t.Fatalf("SetReference error: %v", err)
	}

	// Round 1: Baseline clean (distance ~ 1.7 dB)
	res1, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 1 MatchReference: %v", err)
	}
	dist1 := res1.(map[string]any)["distance_db"].(float64)

	// Round 2: Partial roll-off of air
	daw.airGain = -2.0
	res2, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 2 MatchReference: %v", err)
	}
	dist2 := res2.(map[string]any)["distance_db"].(float64)
	if dist2 >= dist1 {
		t.Fatalf("round 2 distance %.2f did not improve on %.2f", dist2, dist1)
	}

	// Round 3: Match air and presence roll-off of crunch
	daw.airGain = -4.0
	daw.presenceGain = -1.5
	res3, err := a.MatchReference(context.Background(), 1)
	if err != nil {
		t.Fatalf("round 3 MatchReference: %v", err)
	}
	dist3 := res3.(map[string]any)["distance_db"].(float64)
	if dist3 >= dist2 {
		t.Fatalf("round 3 distance %.2f did not improve on %.2f", dist3, dist2)
	}

	conv := res3.(map[string]any)["convergence"].([]map[string]any)
	if len(conv) != 3 {
		t.Fatalf("expected 3 convergence rounds, got %d", len(conv))
	}
	if conv[0]["distance_db"].(float64) <= conv[1]["distance_db"].(float64) ||
		conv[1]["distance_db"].(float64) <= conv[2]["distance_db"].(float64) {
		t.Fatalf("convergence is not monotonically decreasing: %v", conv)
	}
}

// TestGoldenE2E_ScriptedWorkflow verifies a complete workflow where an LLM
// calls match_reference, proposes a plan, listens, and checks the mix.
func TestGoldenE2E_ScriptedWorkflow(t *testing.T) {
	muddWAV := "../../pkg/ears/testdata/mudd.wav"
	cleanWAV := "../../pkg/ears/testdata/clean.wav"
	distWAV := "../../pkg/ears/testdata/distortion.wav"

	daw := newSimulatedEQDAW(t, muddWAV, cleanWAV)
	vis := testVisualizer{daw.tempDir}
	meter := ears.Meter{}

	// Target 0.3 corresponds to (0.3 - 0.5) * 40 = -8.0 dB cut in low & mud
	llm := &scriptedLLM{replies: []core.Reply{
		toolCall("match_reference", map[string]any{"track": 1}),
		toolCall("propose_tone", map[string]any{
			"track": 1,
			"goal":  "cut mud and add top end to match reference",
			"changes": []map[string]any{
				{"param_id": "fx1.p1", "value": 0.3, "why": "cut low end"},
				{"param_id": "fx1.p2", "value": 0.3, "why": "cut mud"},
			},
		}),
		toolCall("apply_plan", map[string]any{"plan_id": "p1"}),
		toolCall("listen", map[string]any{"track": 1}),
		toolCall("check_mix", map[string]any{"track": 1}),
		{Text: "Tone successfully matched against reference and checked in the mix."},
	}}

	a := agent.New(agent.Deps{
		DAW:        daw,
		LLM:        llm,
		Renderer:   daw,
		Measurer:   meter,
		Visualizer: vis,
		Mix:        meter,
	}, agent.Config{})

	if _, err := a.SetReference(distWAV); err != nil {
		t.Fatalf("SetReference error: %v", err)
	}

	ans, err := a.Run(context.Background(), "match reference and sit in the mix", agent.Auto)
	if err != nil {
		t.Fatalf("agent.Run error: %v", err)
	}
	if ans == "" {
		t.Fatalf("empty answer from agent")
	}

	// Verify params were applied through apply_plan
	if daw.lowGain != -8.0 || daw.mudGain != -8.0 {
		t.Errorf("plan was not applied: lowGain=%.1f mudGain=%.1f", daw.lowGain, daw.mudGain)
	}
	// Verify mute/solo restored after check_mix
	if daw.mute != 0 || daw.solo != 0 {
		t.Errorf("mute/solo not restored: mute=%.0f solo=%.0f", daw.mute, daw.solo)
	}
}
