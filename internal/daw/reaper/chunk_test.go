package reaper

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestAmpliTube5AUChunk(t *testing.T) {
	innerXML := `<?xml version="1.0" encoding="UTF-8"?>
<Program Version="1" GUID="773b8ea7-a379-4d24-85a2-3f8d39c5952d" PresetName="Clean Tweed">
  <AmpA Model="JCM800AT4" Bypass="0" PreAmp_JCM800AT4="6.50" Bass_JCM800AT4="5.00" Middle_JCM800AT4="7.00" Treble_JCM800AT4="6.00" Presence_JCM800AT4="5.50" Master_JCM800AT4="8.00" Sensitivity_JCM800AT4="1.00" />
  <CabA Model="4x12_Brit" Bypass="0" CabModel="4x12" HighLevel="0.0" RoomType="Hall" RoomMicType="Condenser" />
  <Studio Cab1_Mic1_Level="-6.0" Cab1_Room_Level="-12.0" />
  <AmpB Model="" Bypass="1" />
  <Slot0 />
  <Preferences GUILoadComplete="1" PresetBPM="120" />
  <Panels />
  <GuiScale />
</Program>`

	// Emulate JUCE binary prefix (e.g. State\x00\x00\x00PresetData)
	juceData := append([]byte("State\x00\x00\x00PresetData"), []byte(innerXML)...)
	b64Juce := base64.StdEncoding.EncodeToString(juceData)

	plistXML := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>jucePluginState</key>
	<data>
` + b64Juce + `
	</data>
</dict>
</plist>`

	b64Plist := base64.StdEncoding.EncodeToString([]byte(plistXML))

	rawAUChunk := `<AU "AU: AmpliTube 5 (IK Multimedia)" "AmpliTube 5" "" 12345
  ` + b64Plist + `
>
`

	// 1. Parse chunk
	params, err := ParseChunk(1, rawAUChunk)
	if err != nil {
		t.Fatalf("ParseChunk failed: %v", err)
	}

	if len(params) == 0 {
		t.Fatalf("expected projected params, got 0")
	}

	// Verify noise attributes are skipped
	for _, p := range params {
		if strings.Contains(strings.ToLower(p.Name), "guid") {
			t.Errorf("found GUID in params: %s", p.Name)
		}
		if strings.Contains(strings.ToLower(p.Name), "version") {
			t.Errorf("found Version in params: %s", p.Name)
		}
		if strings.HasPrefix(p.Name, "Slot") {
			t.Errorf("found Slot in params: %s", p.Name)
		}
		if strings.HasPrefix(p.Name, "AmpB") {
			t.Errorf("found bypassed AmpB in params: %s", p.Name)
		}
	}

	// Verify key parameters were cleaned and projected
	nameMap := map[string]float64{}
	for _, p := range params {
		nameMap[p.Name] = p.Value
	}

	expected := map[string]float64{
		"AmpA PreAmp":            0.65,
		"AmpA Bass":              0.50,
		"AmpA Middle":            0.70,
		"AmpA Treble":            0.60,
		"AmpA Presence":          0.55,
		"AmpA Master":            0.80,
		"CabA HighLevel":         1.00,
		"Studio Cab1_Mic1_Level": 0.85, // (-6 + 40)/40
		"Studio Cab1_Room_Level": 0.70, // (-12 + 40)/40
	}

	for expName, expVal := range expected {
		val, ok := nameMap[expName]
		if !ok {
			t.Errorf("expected projected parameter %q not found", expName)
		} else if val < expVal-0.01 || val > expVal+0.01 {
			t.Errorf("parameter %q: expected ~%.2f, got %.2f", expName, expVal, val)
		}
	}

	// Verify discrete parameter
	var foundRoomType bool
	for _, p := range params {
		if p.Name == "CabA RoomType" {
			foundRoomType = true
			if p.Kind != "discrete" {
				t.Errorf("CabA RoomType kind expected discrete, got %q", p.Kind)
			}
			if p.Text != "Hall" {
				t.Errorf("CabA RoomType text expected Hall, got %q", p.Text)
			}
		}
	}
	if !foundRoomType {
		t.Errorf("CabA RoomType parameter not found")
	}

	// 2. Patch a parameter round-trip (e.g. AmpA PreAmp: find its offset)
	var preAmpOffset int
	for idx, p := range params {
		if p.Name == "AmpA PreAmp" {
			preAmpOffset = idx + 1
			break
		}
	}
	if preAmpOffset == 0 {
		t.Fatalf("AmpA PreAmp offset not found")
	}

	updatedChunk, confirmedNorm, readout, err := PatchChunk(rawAUChunk, preAmpOffset, 0.85)
	if err != nil {
		t.Fatalf("PatchChunk failed: %v", err)
	}

	if confirmedNorm < 0.84 || confirmedNorm > 0.86 {
		t.Errorf("expected confirmedNorm ~0.85, got %f", confirmedNorm)
	}
	if readout != "8.50" {
		t.Errorf("expected readout '8.50', got %q", readout)
	}

	// Re-parse updatedChunk and verify that AmpA PreAmp is now 0.85 (8.50)
	reparsed, err := ParseChunk(1, updatedChunk)
	if err != nil {
		t.Fatalf("re-ParseChunk failed: %v", err)
	}

	var updatedVal float64
	var updatedText string
	for _, p := range reparsed {
		if p.Name == "AmpA PreAmp" {
			updatedVal = p.Value
			updatedText = p.Text
			break
		}
	}
	if updatedVal < 0.84 || updatedVal > 0.86 {
		t.Errorf("reparsed AmpA PreAmp value: expected ~0.85, got %f", updatedVal)
	}
	if updatedText != "8.50" {
		t.Errorf("reparsed AmpA PreAmp text: expected '8.50', got %q", updatedText)
	}
}

func TestJSONChunk(t *testing.T) {
	jsonContent := `{
  "version": 1,
  "guid": "550e8400-e29b-41d4-a716-446655440000",
  "dsp0": {
    "amp": {
      "model": "Line6_Placater",
      "bypass": false,
      "drive": 6.8,
      "bass": 5.2,
      "master": 8.5
    },
    "cab": {
      "mic": "57 Dynamic",
      "level": -3.0
    }
  }
}`

	params, err := ParseChunk(1, jsonContent)
	if err != nil {
		t.Fatalf("ParseChunk on JSON failed: %v", err)
	}

	if len(params) == 0 {
		t.Fatalf("expected parameters from JSON chunk")
	}

	nameMap := map[string]core.Param{}
	for _, p := range params {
		nameMap[p.Name] = p
	}

	if _, ok := nameMap["version"]; ok {
		t.Errorf("expected noise key 'version' to be filtered")
	}
	if _, ok := nameMap["guid"]; ok {
		t.Errorf("expected noise key 'guid' to be filtered")
	}

	driveP, ok := nameMap["dsp0 amp drive"]
	if !ok {
		t.Fatalf("parameter 'dsp0 amp drive' not found")
	}
	if driveP.Value < 0.67 || driveP.Value > 0.69 {
		t.Errorf("drive value expected ~0.68, got %f", driveP.Value)
	}

	// Patch drive to 0.90
	var driveOffset int
	for idx, p := range params {
		if p.Name == "dsp0 amp drive" {
			driveOffset = idx + 1
			break
		}
	}

	updated, confirmedNorm, readout, err := PatchChunk(jsonContent, driveOffset, 0.90)
	if err != nil {
		t.Fatalf("PatchChunk failed: %v", err)
	}
	if confirmedNorm < 0.89 || confirmedNorm > 0.91 {
		t.Errorf("expected confirmed ~0.90, got %f", confirmedNorm)
	}
	if readout != "9.00" {
		t.Errorf("expected readout '9.00', got %q", readout)
	}

	reparsed, err := ParseChunk(1, updated)
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	for _, p := range reparsed {
		if p.Name == "dsp0 amp drive" {
			if p.Value < 0.89 || p.Value > 0.91 {
				t.Errorf("reparsed drive value expected ~0.90, got %f", p.Value)
			}
		}
	}
}

func TestKVChunk(t *testing.T) {
	kvContent := `
# Preset configuration
version=2
guid=e7b4cf53-a71f-4ef6-81cf-1dc240a12e20
Gain=6.5
Bass=5.0
Treble=7.0
Bypass=0
Cabinet=British 4x12
`

	params, err := ParseChunk(1, kvContent)
	if err != nil {
		t.Fatalf("ParseChunk on KV failed: %v", err)
	}

	if len(params) == 0 {
		t.Fatalf("expected parameters from KV chunk")
	}

	nameMap := map[string]core.Param{}
	for _, p := range params {
		nameMap[p.Name] = p
	}

	if _, ok := nameMap["version"]; ok {
		t.Errorf("noise key 'version' not filtered")
	}
	if _, ok := nameMap["guid"]; ok {
		t.Errorf("noise key 'guid' not filtered")
	}

	gainP, ok := nameMap["Gain"]
	if !ok {
		t.Fatalf("Gain not found in KV params")
	}
	if gainP.Value < 0.64 || gainP.Value > 0.66 {
		t.Errorf("expected Gain ~0.65, got %f", gainP.Value)
	}

	// Patch Gain to 0.75
	var gainOffset int
	for idx, p := range params {
		if p.Name == "Gain" {
			gainOffset = idx + 1
			break
		}
	}

	updated, _, _, err := PatchChunk(kvContent, gainOffset, 0.75)
	if err != nil {
		t.Fatalf("PatchChunk KV failed: %v", err)
	}

	reparsed, err := ParseChunk(1, updated)
	if err != nil {
		t.Fatalf("re-parse KV failed: %v", err)
	}
	for _, p := range reparsed {
		if p.Name == "Gain" {
			if p.Value < 0.74 || p.Value > 0.76 {
				t.Errorf("reparsed Gain expected ~0.75, got %f", p.Value)
			}
		}
	}
}

func TestCompressedChunk(t *testing.T) {
	rawXML := `<?xml version="1.0"?><Program><Amp Gain="5.0" Master="8.0"/></Program>`

	// Test Zlib
	var zlibBuf bytes.Buffer
	zw := zlib.NewWriter(&zlibBuf)
	zw.Write([]byte(rawXML))
	zw.Close()

	zlibB64 := base64.StdEncoding.EncodeToString(zlibBuf.Bytes())
	paramsZ, err := ParseChunk(1, zlibB64)
	if err != nil {
		t.Fatalf("ParseChunk zlib failed: %v", err)
	}
	if len(paramsZ) != 2 {
		t.Fatalf("expected 2 params from zlib chunk, got %d", len(paramsZ))
	}

	// Patch via zlib
	updatedZ, _, _, err := PatchChunk(zlibB64, 1, 0.9)
	if err != nil {
		t.Fatalf("PatchChunk zlib failed: %v", err)
	}
	reparsedZ, err := ParseChunk(1, updatedZ)
	if err != nil {
		t.Fatalf("re-parse zlib failed: %v", err)
	}
	if reparsedZ[0].Value < 0.89 || reparsedZ[0].Value > 0.91 {
		t.Errorf("expected patched Gain ~0.9, got %f", reparsedZ[0].Value)
	}

	// Test Gzip
	var gzipBuf bytes.Buffer
	gw := gzip.NewWriter(&gzipBuf)
	gw.Write([]byte(rawXML))
	gw.Close()

	gzipB64 := base64.StdEncoding.EncodeToString(gzipBuf.Bytes())
	paramsG, err := ParseChunk(1, gzipB64)
	if err != nil {
		t.Fatalf("ParseChunk gzip failed: %v", err)
	}
	if len(paramsG) != 2 {
		t.Fatalf("expected 2 params from gzip chunk, got %d", len(paramsG))
	}

	// Patch via gzip
	updatedG, _, _, err := PatchChunk(gzipB64, 2, 0.4)
	if err != nil {
		t.Fatalf("PatchChunk gzip failed: %v", err)
	}
	reparsedG, err := ParseChunk(1, updatedG)
	if err != nil {
		t.Fatalf("re-parse gzip failed: %v", err)
	}
	if reparsedG[1].Value < 0.39 || reparsedG[1].Value > 0.41 {
		t.Errorf("expected patched Master ~0.4, got %f", reparsedG[1].Value)
	}
}

func getParamNames(params []core.Param) []string {
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return names
}

func TestActiveSlotParameters(t *testing.T) {
	xmlPayload := `<Program Version="1" PresetName="SlotTest">
  <StompA1 Bypass="0">
    <Slot0 Bypass="0" Threshold="0.001" Release="500" Depth="-40" />
    <Slot1 />
  </StompA1>
</Program>`

	rawAUChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(xmlPayload)) + `
>`

	params, err := ParseChunk(1, rawAUChunk)
	if err != nil {
		t.Fatalf("ParseChunk failed: %v", err)
	}

	paramMap := map[string]core.Param{}
	for _, p := range params {
		paramMap[p.Name] = p
	}

	// Verify "StompA1 Slot0 Threshold", "StompA1 Slot0 Release", "StompA1 Slot0 Depth" are projected
	for _, exp := range []string{"StompA1 Slot0 Threshold", "StompA1 Slot0 Release", "StompA1 Slot0 Depth"} {
		if _, ok := paramMap[exp]; !ok {
			t.Errorf("expected projected parameter %q not found; got: %v", exp, getParamNames(params))
		}
	}

	// Verify empty Slot1 has no parameters projected
	for k := range paramMap {
		if strings.Contains(k, "Slot1") {
			t.Errorf("unexpected parameter from empty Slot1: %s", k)
		}
	}

	// Test patching "StompA1 Slot0 Threshold" to 0.75
	threshParam := paramMap["StompA1 Slot0 Threshold"]
	_, pNum, _ := threshParam.ID.Parse()
	targetIdx := pNum - 1000

	updatedChunk, confirmedNorm, text, err := PatchChunk(rawAUChunk, targetIdx, 0.75)
	if err != nil {
		t.Fatalf("PatchChunk failed: %v", err)
	}
	if confirmedNorm < 0.74 || confirmedNorm > 0.76 {
		t.Errorf("expected confirmedNorm ~0.75, got %f", confirmedNorm)
	}
	if text != "0.750" {
		t.Errorf("expected text '0.750', got %q", text)
	}

	reparsed, err := ParseChunk(1, updatedChunk)
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	reMap := map[string]core.Param{}
	for _, p := range reparsed {
		reMap[p.Name] = p
	}

	patchedThresh, ok := reMap["StompA1 Slot0 Threshold"]
	if !ok {
		t.Fatalf("StompA1 Slot0 Threshold missing after patch")
	}
	if patchedThresh.Value < 0.74 || patchedThresh.Value > 0.76 {
		t.Errorf("expected patched value ~0.75, got %f", patchedThresh.Value)
	}
}

func TestFindChunkValue(t *testing.T) {
	xmlPayload := `<Program Version="1" PresetName="FindValTest">
  <AmpA Bypass="0" Model="JCM800">
    <Amp PreAmp_JCM800="5.00" Bass_JCM800="4.00" />
  </AmpA>
  <CabA Bypass="0" CabModel="Brit" RoomType="Hall" HighLevel="0.0" />
  <Studio Cab1_Mic1_Level="-6.0" />
</Program>`

	rawChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(xmlPayload)) + `
>`

	params, err := ParseChunk(1, rawChunk)
	if err != nil {
		t.Fatalf("ParseChunk failed: %v", err)
	}

	paramIndexMap := map[string]int{}
	for _, p := range params {
		_, pNum, _ := p.ID.Parse()
		paramIndexMap[p.Name] = pNum - 1000
	}

	// 1. Knob scale (AmpA PreAmp: 0..10 knob scale)
	preampIdx, ok := paramIndexMap["AmpA PreAmp"]
	if !ok {
		t.Fatalf("AmpA PreAmp not found in params: %v", getParamNames(params))
	}
	val85, err := FindChunkValue(rawChunk, preampIdx, "8.5")
	if err != nil {
		t.Fatalf("FindChunkValue '8.5' failed: %v", err)
	}
	if val85 < 0.84 || val85 > 0.86 {
		t.Errorf("expected FindChunkValue '8.5' ~0.85, got %f", val85)
	}

	val65pct, err := FindChunkValue(rawChunk, preampIdx, "65%")
	if err != nil {
		t.Fatalf("FindChunkValue '65%%' failed: %v", err)
	}
	if val65pct < 0.64 || val65pct > 0.66 {
		t.Errorf("expected FindChunkValue '65%%' ~0.65, got %f", val65pct)
	}

	// 2. dB scale (Studio Cab1_Mic1_Level: -40..0 dB)
	micLevelIdx, ok := paramIndexMap["Studio Cab1_Mic1_Level"]
	if !ok {
		t.Fatalf("Studio Cab1_Mic1_Level not found in params: %v", getParamNames(params))
	}
	valDb, err := FindChunkValue(rawChunk, micLevelIdx, "-6 dB")
	if err != nil {
		t.Fatalf("FindChunkValue '-6 dB' failed: %v", err)
	}
	expDbNorm := (-6.0 + 40.0) / 40.0 // 0.85
	if valDb < expDbNorm-0.01 || valDb > expDbNorm+0.01 {
		t.Errorf("expected FindChunkValue '-6 dB' ~%f, got %f", expDbNorm, valDb)
	}

	// 3. Switch scale (AmpA Bypass: On/Off)
	bypassIdx, ok := paramIndexMap["AmpA Bypass"]
	if !ok {
		t.Fatalf("AmpA Bypass not found in params: %v", getParamNames(params))
	}
	valOn, err := FindChunkValue(rawChunk, bypassIdx, "On")
	if err != nil || valOn != 1.0 {
		t.Errorf("expected FindChunkValue 'On' = 1.0, got %f (err: %v)", valOn, err)
	}
	valOff, err := FindChunkValue(rawChunk, bypassIdx, "Off")
	if err != nil || valOff != 0.0 {
		t.Errorf("expected FindChunkValue 'Off' = 0.0, got %f (err: %v)", valOff, err)
	}

	// 4. String discrete parameter (CabA RoomType: "Large Studio")
	roomIdx, ok := paramIndexMap["CabA RoomType"]
	if !ok {
		t.Fatalf("CabA RoomType not found in params: %v", getParamNames(params))
	}
	sentinel, err := FindChunkValue(rawChunk, roomIdx, "Large Studio")
	if err != nil {
		t.Fatalf("FindChunkValue discrete 'Large Studio' failed: %v", err)
	}
	if sentinel <= 0 || sentinel > 1 {
		t.Errorf("expected valid sentinel in (0, 1], got %f", sentinel)
	}

	// Verify PatchChunk with that parameter uses the registered string
	updatedChunk, _, text, err := PatchChunk(rawChunk, roomIdx, sentinel)
	if err != nil {
		t.Fatalf("PatchChunk with discrete sentinel failed: %v", err)
	}
	if text != "Large Studio" {
		t.Errorf("expected text 'Large Studio', got %q", text)
	}
	reparsed, err := ParseChunk(1, updatedChunk)
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	foundRoom := false
	for _, p := range reparsed {
		if p.Name == "CabA RoomType" {
			foundRoom = true
			if p.Text != "Large Studio" {
				t.Errorf("expected reparsed CabA RoomType 'Large Studio', got %q", p.Text)
			}
		}
	}
	if !foundRoom {
		t.Errorf("CabA RoomType missing in reparsed chunk")
	}
}

func TestPatchChunkBatch(t *testing.T) {
	xmlPayload := `<Program Version="1" PresetName="BatchTest">
  <AmpA Bypass="0" Model="JCM800">
    <Amp PreAmp_JCM800="5.00" Bass_JCM800="4.00" Treble_JCM800="6.00" />
  </AmpA>
  <CabA Bypass="0" CabModel="Brit" RoomType="Hall" />
</Program>`

	rawChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(xmlPayload)) + `
>`

	params, err := ParseChunk(1, rawChunk)
	if err != nil {
		t.Fatalf("ParseChunk failed: %v", err)
	}
	paramMap := map[string]int{}
	for _, p := range params {
		_, pNum, _ := p.ID.Parse()
		paramMap[p.Name] = pNum - 1000
	}

	preampIdx := paramMap["AmpA PreAmp"]
	bassIdx := paramMap["AmpA Bass"]
	roomIdx := paramMap["CabA RoomType"]

	// Register a discrete target for roomIdx
	discVal := RegisterDiscreteTarget(1, roomIdx, "Studio B")

	edits := map[int]float64{
		preampIdx: 0.85,    // 8.50
		bassIdx:   0.20,    // 2.00
		roomIdx:   discVal, // "Studio B"
	}

	updatedChunk, applied, err := PatchChunkBatch(rawChunk, edits)
	if err != nil {
		t.Fatalf("PatchChunkBatch failed: %v", err)
	}

	if len(applied) != 3 {
		t.Fatalf("expected 3 applied params, got %d", len(applied))
	}

	if app, ok := applied[preampIdx]; !ok || app.Text != "8.50" {
		t.Errorf("expected PreAmp '8.50', got %+v", app)
	}
	if app, ok := applied[bassIdx]; !ok || app.Text != "2.00" {
		t.Errorf("expected Bass '2.00', got %+v", app)
	}
	if app, ok := applied[roomIdx]; !ok || app.Text != "Studio B" {
		t.Errorf("expected RoomType 'Studio B', got %+v", app)
	}

	// Verify re-parsing
	reparsed, err := ParseChunk(1, updatedChunk)
	if err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	reMap := map[string]core.Param{}
	for _, p := range reparsed {
		reMap[p.Name] = p
	}

	if reMap["AmpA PreAmp"].Text != "8.50" {
		t.Errorf("expected reparsed PreAmp '8.50', got %q", reMap["AmpA PreAmp"].Text)
	}
	if reMap["AmpA Bass"].Text != "2.00" {
		t.Errorf("expected reparsed Bass '2.00', got %q", reMap["AmpA Bass"].Text)
	}
	if reMap["CabA RoomType"].Text != "Studio B" {
		t.Errorf("expected reparsed RoomType 'Studio B', got %q", reMap["CabA RoomType"].Text)
	}
}
