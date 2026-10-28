package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type fakeChain struct{ added []string }

func (f *fakeChain) Catalog(context.Context) ([]core.CatalogEntry, error) {
	return []core.CatalogEntry{{Name: "ReaGate", Kind: "Gate"}, {Name: "ReaEQ", Kind: "EQ"}, {Name: "ReaComp", Kind: "Dynamics"}}, nil
}
func (f *fakeChain) AddFX(_ context.Context, _ int, name string, _ int) (int, error) {
	f.added = append(f.added, name)
	return 3, nil
}
func (f *fakeChain) RemoveFX(context.Context, int, int) (string, error) { return "ReaGate", nil }

type fakeTurboChain struct {
	fakeChain
	autoEnabled bool
	forced      []int
	consentOnce bool
}

func (f *fakeTurboChain) EnableAuto() error {
	f.autoEnabled = true
	return nil
}

func (f *fakeTurboChain) AddFX(ctx context.Context, track int, name string, pos int) (int, error) {
	if !f.autoEnabled && !f.consentOnce {
		f.consentOnce = true
		return 0, core.ErrNeedsConsent
	}
	return f.fakeChain.AddFX(ctx, track, name, pos)
}

func (f *fakeTurboChain) RemoveFX(ctx context.Context, track, fx int) (string, error) {
	return "", errors.New("ReaEQ was not added by the agent; only plugins it added can be removed")
}

func (f *fakeTurboChain) ForceRemoveFX(ctx context.Context, track, fx int) (string, error) {
	f.forced = append(f.forced, fx)
	return "ReaEQ", nil
}

type fakeMoverChain struct {
	fakeChain
	movedFrom int
	movedTo   int
}

func (f *fakeMoverChain) MoveFX(_ context.Context, _, from, to int) error {
	f.movedFrom = from
	f.movedTo = to
	return nil
}

type fakePresetChain struct {
	fakeChain
	current string
	list    []string
	loaded  string
	saved   string
}

func (f *fakePresetChain) Presets(_ context.Context, _, _ int) (string, []string, error) {
	return f.current, f.list, nil
}

func (f *fakePresetChain) SetPreset(_ context.Context, _, _ int, preset string) error {
	f.loaded = preset
	f.current = preset
	return nil
}

func (f *fakePresetChain) SavePresetAs(_ context.Context, _, _ int, name string) error {
	f.saved = name
	f.current = name
	f.list = append(f.list, name)
	return nil
}

type fakeTurboPresetChain struct {
	fakePresetChain
	autoEnabled bool
}

func (f *fakeTurboPresetChain) EnableAuto() error {
	f.autoEnabled = true
	return nil
}

func (f *fakeTurboPresetChain) ForceRemoveFX(context.Context, int, int) (string, error) {
	return "ReaEQ", nil
}

type memSnapshots struct {
	saved []core.Snapshot
}

func (m *memSnapshots) SaveSnapshot(s core.Snapshot) error {
	m.saved = append(m.saved, s)
	return nil
}

func (m *memSnapshots) Snapshot(string) (*core.Snapshot, error) { return nil, nil }
func (m *memSnapshots) Snapshots() ([]core.Snapshot, error)     { return m.saved, nil }

func TestAddPluginOnlyFromCatalog(t *testing.T) {
	fc := &fakeChain{}
	a := New(Deps{DAW: newMemDAW(), Chain: fc}, Config{})
	if _, err := a.addPlugin(context.Background(), 1, "Some Random Synth", 0); err == nil {
		t.Fatal("a plugin outside the catalog was accepted")
	}
	out, err := a.addPlugin(context.Background(), 1, "ReaGate", 1)
	if err != nil || len(fc.added) != 1 {
		t.Fatalf("add: %v, %v", out, err)
	}
	if out.(map[string]any)["chain"] == nil {
		t.Fatal("the new chain was not read back")
	}
}

func TestFindPluginsByKind(t *testing.T) {
	a := New(Deps{DAW: newMemDAW(), Chain: &fakeChain{}}, Config{})
	out, err := a.findPlugins(context.Background(), "gate", 10)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["matches"] != 1 || m["installed"] != 3 {
		t.Fatalf("got %v", m)
	}
}

func TestTurboModeAutoConsentAndForceRemove(t *testing.T) {
	tc := &fakeTurboChain{}
	snaps := &memSnapshots{}
	ev := &sink{}
	a := New(Deps{DAW: newMemDAW(), Chain: tc, Snapshots: snaps, Sink: ev}, Config{})
	a.mu.Lock()
	a.mode = Turbo
	a.request = "heavy tone"
	a.mu.Unlock()

	// 1. AddFX in Turbo mode should call EnableAuto and retry if consent was needed
	out, err := a.addPlugin(context.Background(), 1, "ReaGate", 1)
	if err != nil {
		t.Fatalf("addPlugin error: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["needs_permission"] == true {
		t.Fatalf("addPlugin blocked on consent in turbo mode: %v", out)
	}
	if !tc.autoEnabled {
		t.Fatal("expected EnableAuto to have been called")
	}

	// 2. ForceRemoveFX in Turbo mode should remove even pre-existing user plugins and snapshot
	rmOut, err := a.removePlugin(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("removePlugin error: %v", err)
	}
	rmMap := rmOut.(map[string]any)
	if rmMap["removed"] != "ReaEQ" {
		t.Fatalf("expected ReaEQ to be removed, got: %v", rmMap)
	}
	if len(tc.forced) != 1 || tc.forced[0] != 2 {
		t.Fatalf("expected ForceRemoveFX(1, 2) to be called, got: %v", tc.forced)
	}
	if len(snaps.saved) == 0 {
		t.Fatal("expected autoSnapshot to be called before force-removing")
	}
}

func TestDefaultModeConsentOnUserPlugin(t *testing.T) {
	tc := &fakeTurboChain{}
	ev := &sink{}
	a := New(Deps{DAW: newMemDAW(), Chain: tc, Sink: ev}, Config{})
	a.mu.Lock()
	a.mode = Auto
	a.mu.Unlock()

	out, err := a.removePlugin(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["needs_permission"] != true {
		t.Fatalf("expected needs_permission: true, got: %v", out)
	}

	// Check consent event was emitted
	var consentEmitted bool
	for _, k := range ev.kinds {
		if k == "consent" {
			consentEmitted = true
			break
		}
	}
	if !consentEmitted {
		t.Fatalf("expected 'consent' event to be emitted, got kinds: %v", ev.kinds)
	}
}

func TestMovePlugin(t *testing.T) {
	mc := &fakeMoverChain{}
	ev := &sink{}
	a := New(Deps{DAW: newMemDAW(), Chain: mc, Sink: ev}, Config{})

	out, err := a.movePlugin(context.Background(), 1, 2, 1)
	if err != nil {
		t.Fatalf("movePlugin: %v", err)
	}
	m := out.(map[string]any)
	if m["moved"] != 2 || m["to"] != 1 {
		t.Fatalf("unexpected move output: %v", m)
	}
	if mc.movedFrom != 2 || mc.movedTo != 1 {
		t.Fatalf("mover got from=%d to=%d", mc.movedFrom, mc.movedTo)
	}
}

func TestPresets(t *testing.T) {
	pc := &fakePresetChain{
		current: "Clean",
		list:    []string{"Clean", "Crunch", "Lead"},
	}
	ev := &sink{}
	snaps := &memSnapshots{}
	a := New(Deps{DAW: newMemDAW(), Chain: pc, Snapshots: snaps, Sink: ev}, Config{})

	// list_presets
	lOut, err := a.listPresets(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("listPresets: %v", err)
	}
	lMap := lOut.(map[string]any)
	if lMap["current"] != "Clean" {
		t.Fatalf("expected current Clean, got %v", lMap["current"])
	}
	presets := lMap["presets"].([]string)
	if len(presets) != 3 || presets[1] != "Crunch" {
		t.Fatalf("presets list: %v", presets)
	}

	// load_preset
	a.mu.Lock()
	a.request = "more grit"
	a.mu.Unlock()
	loadOut, err := a.loadPreset(context.Background(), 1, 1, "Crunch")
	if err != nil {
		t.Fatalf("loadPreset: %v", err)
	}
	loadMap := loadOut.(map[string]any)
	if loadMap["preset"] != "Crunch" {
		t.Fatalf("expected preset Crunch, got %v", loadMap)
	}
	if pc.loaded != "Crunch" {
		t.Fatalf("expected fakePresetChain to have loaded Crunch, got %q", pc.loaded)
	}
	if len(snaps.saved) == 0 {
		t.Fatal("expected autoSnapshot on loadPreset")
	}
}

func TestSavePresetAsRequiresConsentAndVerifiesReadback(t *testing.T) {
	pc := &fakePresetChain{current: "Clean", list: []string{"Clean"}}
	events := &sink{}
	a := New(Deps{DAW: newMemDAW(), Chain: pc, Sink: events}, Config{})

	if out, err := a.savePresetAs(context.Background(), 1, 1, "  Verse  "); err != nil {
		t.Fatal(err)
	} else if m, ok := out.(map[string]any); !ok || m["needs_permission"] != true {
		t.Fatalf("expected consent request, got %#v", out)
	}
	if pc.saved != "" {
		t.Fatalf("saved before consent: %q", pc.saved)
	}
	a.Allow(kindPresetSave)
	out, err := a.savePresetAs(context.Background(), 1, 1, "  Verse  ")
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if pc.saved != "Verse" || m["saved"] != true || m["name"] != "Verse" {
		t.Fatalf("save result did not confirm trimmed name: saved=%q result=%v", pc.saved, m)
	}
}

func TestTurboModePresetAutoConsent(t *testing.T) {
	tpc := &fakeTurboPresetChain{
		fakePresetChain: fakePresetChain{
			current: "Clean",
			list:    []string{"Clean", "Lead"},
		},
	}
	a := New(Deps{DAW: newMemDAW(), Chain: tpc}, Config{})
	a.mu.Lock()
	a.mode = Turbo
	a.mu.Unlock()

	_, err := a.loadPreset(context.Background(), 1, 1, "Lead")
	if err != nil {
		t.Fatalf("loadPreset error: %v", err)
	}
	if !tpc.autoEnabled {
		t.Fatal("expected EnableAuto to be called in Turbo mode during loadPreset")
	}
}

type fakeSwapperChain struct {
	fakeChain
	autoEnabled bool
	lastSlot    string
	lastModel   string
}

func (f *fakeSwapperChain) EnableAuto() error {
	f.autoEnabled = true
	return nil
}

func (f *fakeSwapperChain) ForceRemoveFX(context.Context, int, int) (string, error) {
	return "ReaEQ", nil
}

func (f *fakeSwapperChain) PluginModels(_ context.Context, _, _ int, _ string) ([]core.PluginSlot, error) {
	return []core.PluginSlot{
		{Slot: "AmpA", Current: "Brit800", Available: []string{"Brit800", "Diezel", "Tweed"}},
		{Slot: "CabA", Current: "4x12", Available: []string{"4x12", "2x12", "1x12"}},
	}, nil
}

func (f *fakeSwapperChain) SetPluginModel(_ context.Context, _, _ int, slot, model string) ([]core.Param, error) {
	f.lastSlot = slot
	f.lastModel = model
	return []core.Param{{ID: "fx1.p1001", Name: slot + " Gain", Value: 0.5}}, nil
}

func TestModelSwapperTools(t *testing.T) {
	swapper := &fakeSwapperChain{}
	snaps := &memSnapshots{}
	ev := &sink{}
	a := New(Deps{DAW: newMemDAW(), Chain: swapper, Snapshots: snaps, Sink: ev}, Config{})
	a.mu.Lock()
	a.mode = Turbo
	a.request = "diezel high gain"
	a.mu.Unlock()

	// 1. list_plugin_models
	lOut, err := a.listPluginModels(context.Background(), 1, 1, "AmpA")
	if err != nil {
		t.Fatalf("listPluginModels: %v", err)
	}
	lMap := lOut.(map[string]any)
	slots := lMap["slots"].([]core.PluginSlot)
	if len(slots) != 2 || slots[0].Slot != "AmpA" {
		t.Fatalf("unexpected slots: %+v", slots)
	}

	// 2. set_plugin_model (in Turbo mode)
	sOut, err := a.setPluginModel(context.Background(), 1, 1, "AmpA", "Diezel")
	if err != nil {
		t.Fatalf("setPluginModel: %v", err)
	}
	sMap := sOut.(map[string]any)
	if sMap["slot"] != "AmpA" || sMap["model"] != "Diezel" {
		t.Fatalf("unexpected swap output: %+v", sMap)
	}
	if swapper.lastSlot != "AmpA" || swapper.lastModel != "Diezel" {
		t.Fatalf("swapper called with %s %s", swapper.lastSlot, swapper.lastModel)
	}
	if !swapper.autoEnabled {
		t.Fatal("expected EnableAuto in turbo mode on set_plugin_model")
	}
	if len(snaps.saved) == 0 {
		t.Fatal("expected autoSnapshot on set_plugin_model")
	}

	// check event
	var swapEvent bool
	for _, k := range ev.kinds {
		if k == "model_swap" {
			swapEvent = true
			break
		}
	}
	if !swapEvent {
		t.Fatal("expected model_swap event emitted")
	}
}

type fakeSessionDAW struct {
	*memDAW
	createdTracks []string
	duplicated    int
	deletedTracks []int
	renamed       map[int]string
	insertedMedia []string
	sends         []string
	transports    []string
}

func (f *fakeSessionDAW) CreateTrack(_ context.Context, name string, _ int) (int, error) {
	f.createdTracks = append(f.createdTracks, name)
	return len(f.createdTracks) + 1, nil
}
func (f *fakeSessionDAW) DuplicateTrack(_ context.Context, track int, _ string) (int, error) {
	f.duplicated = track
	return 99, nil
}
func (f *fakeSessionDAW) DeleteTrack(_ context.Context, track int) error {
	f.deletedTracks = append(f.deletedTracks, track)
	return nil
}
func (f *fakeSessionDAW) RenameTrack(_ context.Context, track int, name string) error {
	if f.renamed == nil {
		f.renamed = map[int]string{}
	}
	f.renamed[track] = name
	return nil
}
func (f *fakeSessionDAW) InsertMedia(_ context.Context, track int, path string, pos float64) error {
	f.insertedMedia = append(f.insertedMedia, fmt.Sprintf("%d:%s@%.1f", track, path, pos))
	return nil
}
func (f *fakeSessionDAW) CreateSend(_ context.Context, from, to int, lvl float64) error {
	f.sends = append(f.sends, fmt.Sprintf("%d->%d:%.1fdB", from, to, lvl))
	return nil
}
func (f *fakeSessionDAW) SetTransport(_ context.Context, act string, bpm, pos, _, _ float64) error {
	f.transports = append(f.transports, fmt.Sprintf("%s:%.0fbpm@%.1fs", act, bpm, pos))
	return nil
}

func TestSessionControllerTools(t *testing.T) {
	daw := &fakeSessionDAW{memDAW: newMemDAW()}
	ev := &sink{}
	a := New(Deps{DAW: daw, Sink: ev}, Config{})

	// manage_track create
	cr, err := a.manageTrack(context.Background(), "create", 1, "Bass")
	if err != nil || cr.(map[string]any)["name"] != "Bass" {
		t.Fatalf("create track: %v, %v", cr, err)
	}

	// manage_track duplicate
	dup, err := a.manageTrack(context.Background(), "duplicate", 1, "Guitar Copy")
	if err != nil || dup.(map[string]any)["track"] != 99 {
		t.Fatalf("duplicate track: %v, %v", dup, err)
	}

	// manage_track rename
	ren, err := a.manageTrack(context.Background(), "rename", 2, "Lead Guitar")
	if err != nil || daw.renamed[2] != "Lead Guitar" {
		t.Fatalf("rename track: %v, %v", ren, err)
	}

	// manage_track delete in Default mode requires confirmation
	delBlocked, err := a.manageTrack(context.Background(), "delete", 2, "")
	if err != nil || delBlocked.(map[string]any)["needs_permission"] != true {
		t.Fatalf("delete track in default mode should require permission: %v, %v", delBlocked, err)
	}
	if len(daw.deletedTracks) != 0 {
		t.Fatal("delete track should not have executed in default mode")
	}

	// manage_track delete in Turbo mode succeeds without prompt
	a.mu.Lock()
	a.mode = Turbo
	a.mu.Unlock()
	del, err := a.manageTrack(context.Background(), "delete", 2, "")
	if err != nil || len(daw.deletedTracks) != 1 {
		t.Fatalf("delete track in turbo mode: %v, %v", del, err)
	}

	// insert_media
	ins, err := a.insertMedia(context.Background(), 1, "/path/to/riff.wav", 4.0)
	if err != nil || len(daw.insertedMedia) != 1 {
		t.Fatalf("insert_media: %v, %v", ins, err)
	}

	// route_send
	snd, err := a.routeSend(context.Background(), 1, 3, -6.0)
	if err != nil || len(daw.sends) != 1 {
		t.Fatalf("route_send: %v, %v", snd, err)
	}

	// set_transport
	tr, err := a.setTransport(context.Background(), "tempo", 140, 0, 0, 0)
	if err != nil || len(daw.transports) != 1 {
		t.Fatalf("set_transport: %v, %v", tr, err)
	}
}

func TestConvergenceTrackingListen(t *testing.T) {
	daw, f, ev := newMemDAW(), files{t.TempDir()}, &sink{}
	a := New(Deps{DAW: daw, Renderer: f, Measurer: f, Visualizer: f, Sink: ev}, Config{})

	// Round 1
	out1, _, err := a.listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("listen 1: %v", err)
	}
	m1 := out1.(map[string]any)
	c1, ok := m1["convergence"].([]map[string]any)
	if !ok || len(c1) != 1 {
		t.Fatalf("expected 1 convergence entry, got: %+v", m1["convergence"])
	}

	// Round 2
	out2, _, err := a.listen(context.Background(), 1)
	if err != nil {
		t.Fatalf("listen 2: %v", err)
	}
	m2 := out2.(map[string]any)
	c2, ok := m2["convergence"].([]map[string]any)
	if !ok || len(c2) != 2 {
		t.Fatalf("expected 2 convergence entries, got: %+v", m2["convergence"])
	}
	if c2[1]["round"] != 2 || c2[1]["label"] != "Round 2" {
		t.Fatalf("round 2 malformed: %+v", c2[1])
	}
}

func TestCompactChain(t *testing.T) {
	// Build an FX with 5 unnamed params and 45 named params
	var params []core.Param
	for i := 1; i <= 5; i++ {
		params = append(params, core.Param{
			ID:      core.MakeParamID(1, i),
			Name:    fmt.Sprintf("Param %d", i),
			Value:   0.1234567,
			Unnamed: true,
		})
	}
	for i := 6; i <= 50; i++ {
		params = append(params, core.Param{
			ID:    core.MakeParamID(1, i),
			Name:  fmt.Sprintf("Control %d", i),
			Value: 0.333333333,
			Text:  "33%",
		})
	}

	fx := []core.FX{
		{Index: 1, Name: "MegaSynth", Params: params},
	}

	compact := compactChain(fx)
	if len(compact) != 1 {
		t.Fatalf("expected 1 fx, got %d", len(compact))
	}
	f := compact[0]
	if f["unnamed_count"] != 5 {
		t.Fatalf("expected unnamed_count=5, got %v", f["unnamed_count"])
	}
	namedList, ok := f["params"].([]map[string]any)
	if !ok || len(namedList) != 40 {
		t.Fatalf("expected capped at 40 named params, got %d", len(namedList))
	}
	if f["more_params"] != 5 {
		t.Fatalf("expected more_params=5, got %v", f["more_params"])
	}
	if f["hint"] != "use search_params to find specific controls" {
		t.Fatalf("unexpected hint: %v", f["hint"])
	}
	// Verify rounding
	if namedList[0]["value"] != 0.333 {
		t.Fatalf("expected rounded 0.333, got %v", namedList[0]["value"])
	}
}

type fakeBatchDAW struct {
	*memDAW
	batchCalled int
	batchLen    int
}

func (f *fakeBatchDAW) SetParams(_ context.Context, _ int, cs []core.Change) ([]core.Applied, error) {
	f.batchCalled++
	f.batchLen = len(cs)
	var out []core.Applied
	for _, c := range cs {
		f.vals[c.Param] = c.Value
		out = append(out, core.Applied{
			Param:     c.Param,
			Sent:      c.Value,
			Confirmed: c.Value,
			Text:      fmt.Sprintf("%.2f", c.Value),
			Status:    core.StatusConfirmed,
		})
	}
	return out, nil
}

func TestBatchSetterApplyPlan(t *testing.T) {
	daw := &fakeBatchDAW{memDAW: newMemDAW()}
	a := New(Deps{DAW: daw}, Config{})

	// propose a plan
	_, _, err := a.propose(context.Background(), 1, "batch test", []core.Change{
		{Param: "fx1.p3", Value: 0.75, Why: "gain"},
		{Param: "fx1.p4", Value: 0.60, Why: "bass"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// apply the plan
	out, err := a.applyPlan(context.Background(), "p1")
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	if daw.batchCalled != 1 || daw.batchLen != 2 {
		t.Fatalf("batch setter not called as expected: called=%d, len=%d", daw.batchCalled, daw.batchLen)
	}
	m := out.(map[string]any)
	results := m["applied"].([]core.Applied)
	if len(results) != 2 || results[0].Text != "0.75" {
		t.Fatalf("unexpected results: %+v", results)
	}
	// Verify cached chain was updated in-place
	a.mu.Lock()
	ch := a.chains[1]
	a.mu.Unlock()
	p, ok := find(ch, "fx1.p3")
	if !ok || p.Value != 0.75 || p.Text != "0.75" {
		t.Fatalf("in-place cache update failed: %+v", p)
	}
}

func TestDefaultModeAutoConsentForSafeOps(t *testing.T) {
	tc := &fakeTurboChain{}
	a := New(Deps{DAW: newMemDAW(), Chain: tc}, Config{})
	// Default mode (Auto)
	a.mu.Lock()
	a.mode = Auto
	a.mu.Unlock()

	// addPlugin in Default mode should auto-enable consent
	out, err := a.addPlugin(context.Background(), 1, "ReaGate", 1)
	if err != nil || out.(map[string]any)["needs_permission"] == true {
		t.Fatalf("addPlugin in Default mode should auto-enable consent: %v, %v", out, err)
	}
	if !tc.autoEnabled {
		t.Fatal("expected EnableAuto() called in Default mode for addPlugin")
	}

	// removePlugin for user-placed plugin should still require permission
	rmOut, err := a.removePlugin(context.Background(), 1, 2)
	if err != nil || rmOut.(map[string]any)["needs_permission"] != true {
		t.Fatalf("removePlugin in Default mode should require permission for user plugins: %v, %v", rmOut, err)
	}
}
