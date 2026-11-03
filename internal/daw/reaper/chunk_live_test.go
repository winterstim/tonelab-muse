//go:build live

package reaper

import (
	"os"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func getLiveParamNames(params []core.Param) []string {
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return names
}

func TestRealAmpliTube5AUChunk(t *testing.T) {
	rawData, err := os.ReadFile(os.Getenv("REAPER_RPP_FIXTURE"))
	if err != nil {
		t.Skip("set REAPER_RPP_FIXTURE to run the real project fixture test")
	}

	// Extract lines starting at `<AU "AU: AmpliTube 5` up through footer lines (`WAK 0 0`)
	lines := strings.Split(string(rawData), "\n")
	var chunkLines []string
	inChunk := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, `<AU "AU: AmpliTube 5`) {
			inChunk = true
		}
		if inChunk {
			chunkLines = append(chunkLines, l)
			if strings.HasPrefix(trimmed, "WAK ") {
				break
			}
		}
	}
	if len(chunkLines) == 0 {
		t.Fatalf("could not extract AmpliTube 5 AU chunk from test-amplitube.rpp")
	}
	rawChunk := strings.Join(chunkLines, "\n")

	// 2. ParseChunk on real chunk
	params, err := ParseChunk(1, rawChunk)
	if err != nil {
		t.Fatalf("ParseChunk failed on real AmpliTube 5 AU chunk: %v", err)
	}
	if len(params) == 0 {
		t.Fatalf("expected projected params from real AU chunk, got 0")
	}

	paramMap := map[string]core.Param{}
	for _, p := range params {
		paramMap[p.Name] = p
	}

	// Verify key parameters: AmpA PreAmp, CabA, Studio
	preampParam, ok := paramMap["AmpA PreAmp"]
	if !ok {
		t.Fatalf("expected 'AmpA PreAmp' in projected params; got: %v", getLiveParamNames(params))
	}
	t.Logf("Found real AmpA PreAmp: text=%q, norm=%f", preampParam.Text, preampParam.Value)

	hasCab := false
	for k := range paramMap {
		if strings.HasPrefix(k, "CabA") {
			hasCab = true
			break
		}
	}
	if !hasCab {
		t.Errorf("expected CabA parameter in projected params")
	}

	hasStudio := false
	for k := range paramMap {
		if strings.HasPrefix(k, "Studio") {
			hasStudio = true
			break
		}
	}
	if !hasStudio {
		t.Errorf("expected Studio parameter in projected params")
	}

	// 3. Patch AmpA PreAmp to 8.50 (norm = 0.85)
	_, pNum, _ := preampParam.ID.Parse()
	targetIdx := pNum - 1000

	updatedChunk, confirmedNorm, text, err := PatchChunk(rawChunk, targetIdx, 0.85)
	if err != nil {
		t.Fatalf("PatchChunk failed: %v", err)
	}
	if confirmedNorm < 0.84 || confirmedNorm > 0.86 {
		t.Errorf("expected confirmedNorm ~0.85, got %f", confirmedNorm)
	}
	if text != "8.50" {
		t.Errorf("expected text '8.50', got %q", text)
	}

	// 4. Verify envelope preservation:
	// - REAPER footer lines (FLOATPOS, FXID, WAK) are preserved
	if !strings.Contains(updatedChunk, "FLOATPOS ") || !strings.Contains(updatedChunk, "WAK 0 0") {
		t.Errorf("REAPER footer lines lost in patched chunk")
	}

	// 5. Re-parse patched chunk and verify AmpA PreAmp is 8.50
	reparsed, err := ParseChunk(1, updatedChunk)
	if err != nil {
		t.Fatalf("re-parsing patched chunk failed: %v", err)
	}
	reMap := map[string]core.Param{}
	for _, p := range reparsed {
		reMap[p.Name] = p
	}
	newPreamp, ok := reMap["AmpA PreAmp"]
	if !ok {
		t.Fatalf("AmpA PreAmp missing after patch")
	}
	if newPreamp.Text != "8.50" {
		t.Errorf("expected patched text '8.50', got %q", newPreamp.Text)
	}
	if newPreamp.Value < 0.84 || newPreamp.Value > 0.86 {
		t.Errorf("expected patched norm ~0.85, got %f", newPreamp.Value)
	}
}
