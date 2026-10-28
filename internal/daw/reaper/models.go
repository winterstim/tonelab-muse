package reaper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type ampTemplate struct {
	Name      string
	ModelUUID string
	InnerXML  string
}

type cabTemplate struct {
	Name         string
	CabModelUUID string
	InnerXML     string
}

type stompTemplate struct {
	Name      string
	StompUUID string
	SlotAttrs map[string]string
}

type modelCatalog struct {
	presets        []string
	presetFiles    map[string]string
	ampTemplates   map[string]ampTemplate
	cabTemplates   map[string]cabTemplate
	stompTemplates map[string]stompTemplate
}

var catalogCache = struct {
	sync.RWMutex
	m map[string]*modelCatalog
}{m: map[string]*modelCatalog{}}

func findPresetDirs(chunk string, extraDirs []string) []string {
	var dirs []string
	seen := map[string]bool{}

	addDir := func(d string) {
		d = filepath.Clean(d)
		if seen[d] {
			return
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
			seen[d] = true
		}
	}

	for _, d := range extraDirs {
		addDir(d)
	}

	// 1. Check PresetPath in chunk
	rePath := regexp.MustCompile(`PresetPath="([^"]+)"`)
	if m := rePath.FindStringSubmatch(chunk); len(m) > 1 {
		p := m[1]
		cur := filepath.Dir(p)
		for i := 0; i < 5; i++ {
			if strings.EqualFold(filepath.Base(cur), "presets") {
				addDir(cur)
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}

	// 2. Standard paths
	home, _ := os.UserHomeDir()
	if home != "" {
		addDir(filepath.Join(home, "Documents", "IK Multimedia", "AmpliTube 5", "Presets"))
		addDir(filepath.Join(home, "Library", "Audio", "Presets"))
		addDir(filepath.Join(home, "AppData", "Roaming", "IK Multimedia", "AmpliTube 5", "Presets"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		addDir(filepath.Join(appData, "IK Multimedia", "AmpliTube 5", "Presets"))
	}

	return dirs
}

func loadModelCatalog(dirs []string) *modelCatalog {
	if len(dirs) == 0 {
		return &modelCatalog{
			presetFiles:  map[string]string{},
			ampTemplates: map[string]ampTemplate{},
			cabTemplates: map[string]cabTemplate{},
			stompTemplates: map[string]stompTemplate{
				"Empty": {
					Name:      "Empty",
					StompUUID: "773b8ea7-b54a-4a3c-99df-ffbbf6d29271",
					SlotAttrs: nil,
				},
			},
		}
	}
	key := strings.Join(dirs, "|")
	catalogCache.RLock()
	cat, ok := catalogCache.m[key]
	catalogCache.RUnlock()
	if ok {
		return cat
	}

	cat = &modelCatalog{
		presetFiles:    map[string]string{},
		ampTemplates:   map[string]ampTemplate{},
		cabTemplates:   map[string]cabTemplate{},
		stompTemplates: map[string]stompTemplate{},
	}

	// Empty stomp is always available
	cat.stompTemplates["Empty"] = stompTemplate{
		Name:      "Empty",
		StompUUID: "773b8ea7-b54a-4a3c-99df-ffbbf6d29271",
		SlotAttrs: nil,
	}

	fileCount := 0
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if fileCount >= 200 {
				return filepath.SkipAll
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".at5p" && ext != ".xml" && ext != ".json" {
				return nil
			}

			fileCount++
			base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			cat.presets = append(cat.presets, base)
			cat.presetFiles[base] = path

			// Parse templates from preset content
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			parsePresetTemplates(string(data), cat)
			return nil
		})
	}

	sort.Strings(cat.presets)
	catalogCache.Lock()
	catalogCache.m[key] = cat
	catalogCache.Unlock()
	return cat
}

func parsePresetTemplates(content string, cat *modelCatalog) {
	// 1. Amp templates: <AmpA ... Model="UUID"> <Amp ... /> </AmpA>
	for _, ampTag := range []string{"AmpA", "AmpB", "AmpC", "Amp"} {
		re := regexp.MustCompile(`(?s)<` + ampTag + `\b[^>]*\bModel="([^"]+)"[^>]*>([\s\S]*?)</` + ampTag + `>`)
		matches := re.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			modelUUID := m[1]
			body := strings.TrimSpace(m[2])
			if !strings.HasPrefix(body, "<Amp") {
				continue
			}
			// Extract model name from attribute suffix e.g. Ch1Gain_Diezel="...", PreAmp_JCM800AT4="..."
			reModel := regexp.MustCompile(`_([A-Za-z0-9]+)="`)
			modelMatches := reModel.FindAllStringSubmatch(body, -1)
			modelName := ""
			for _, mm := range modelMatches {
				cand := mm[1]
				if !strings.EqualFold(cand, "Level") && !strings.EqualFold(cand, "Type") && !strings.EqualFold(cand, "Mode") && len(cand) >= 3 {
					modelName = cand
					break
				}
			}
			if modelName == "" {
				modelName = modelUUID
			}
			if _, exists := cat.ampTemplates[modelName]; !exists {
				cat.ampTemplates[modelName] = ampTemplate{
					Name:      modelName,
					ModelUUID: modelUUID,
					InnerXML:  body,
				}
			}
		}
	}

	// 2. Cab templates: <CabA ... Model="UUID" CabModel="CabUUID"> <Cab ... /> </CabA>
	for _, cabTag := range []string{"CabA", "CabB", "CabC", "Cab"} {
		re := regexp.MustCompile(`(?s)<` + cabTag + `\b[^>]*\bCabModel="([^"]+)"[^>]*>([\s\S]*?)</` + cabTag + `>`)
		matches := re.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			cabUUID := m[1]
			body := strings.TrimSpace(m[2])
			if !strings.HasPrefix(body, "<Cab") {
				continue
			}
			name := cabUUID
			reRoom := regexp.MustCompile(`RoomType="([^"]+)"`)
			if rm := reRoom.FindStringSubmatch(body); len(rm) > 1 && rm[1] != "" {
				name = rm[1]
			}
			if _, exists := cat.cabTemplates[name]; !exists {
				cat.cabTemplates[name] = cabTemplate{
					Name:         name,
					CabModelUUID: cabUUID,
					InnerXML:     body,
				}
			}
		}
	}

	// 3. Stomp templates: <StompA1 ... Stomp0="UUID" ...> <Slot0 ...attrs... /> </StompA1>
	reOpen := regexp.MustCompile(`<((?:Stomp|LoopFx|Rack)[A-Za-z0-9]+)\b([^>]*)>`)
	openMatches := reOpen.FindAllStringSubmatchIndex(content, -1)
	for _, om := range openMatches {
		bankName := content[om[2]:om[3]]
		bankAttrs := content[om[4]:om[5]]
		closeTag := "</" + bankName + ">"
		rest := content[om[1]:]
		closeIdx := strings.Index(rest, closeTag)
		if closeIdx < 0 {
			continue
		}
		bankBody := rest[:closeIdx]

		for slotIdx := 0; slotIdx < 6; slotIdx++ {
			stompKey := fmt.Sprintf("Stomp%d", slotIdx)
			slotTag := fmt.Sprintf("Slot%d", slotIdx)

			reStompUUID := regexp.MustCompile(stompKey + `="([^"]+)"`)
			mUUID := reStompUUID.FindStringSubmatch(bankAttrs)
			if len(mUUID) <= 1 || mUUID[1] == "773b8ea7-b54a-4a3c-99df-ffbbf6d29271" || mUUID[1] == "" {
				continue
			}
			stompUUID := mUUID[1]

			reSlot := regexp.MustCompile(`(?s)<` + slotTag + `\b([^>]*)/?>`)
			mSlot := reSlot.FindStringSubmatch(bankBody)
			if len(mSlot) <= 1 {
				continue
			}

			// Parse slot attributes
			attrs := map[string]string{}
			reAttr := regexp.MustCompile(`([A-Za-z0-9_]+)="([^"]*)"`)
			for _, am := range reAttr.FindAllStringSubmatch(mSlot[1], -1) {
				attrs[am[1]] = am[2]
			}
			if len(attrs) == 0 {
				continue
			}

			// Identify stomp name from attributes
			stompName := identifyStompName(attrs)
			if stompName != "" && stompName != "Empty" {
				if _, exists := cat.stompTemplates[stompName]; !exists {
					cat.stompTemplates[stompName] = stompTemplate{
						Name:      stompName,
						StompUUID: stompUUID,
						SlotAttrs: attrs,
					}
				}
			}
		}
	}
}

func identifyStompName(attrs map[string]string) string {
	has := func(k string) bool {
		_, ok := attrs[k]
		return ok
	}
	if has("Threshold") && has("Release") {
		return "Gate (Threshold, Release, Depth)"
	}
	if has("Drive") && has("Tone") {
		return "Drive (Drive, Volume, Tone)"
	}
	if has("Comp") {
		return "Comp (Comp, Level)"
	}
	if has("Chorus") || (has("Rate") && has("Depth")) {
		return "Chorus (Rate, Depth, Level)"
	}
	if has("Time") && has("Feedback") {
		return "Delay (Time, Feedback, Level)"
	}
	if has("Band100") || has("Band100Hz") || has("Band200") {
		return "EQ (Graphic)"
	}
	// Fallback to first 3 non-bypass attributes
	var keys []string
	for k := range attrs {
		if k != "Bypass" && k != "Model" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 3 {
		keys = keys[:3]
	}
	if len(keys) == 0 {
		return "Stomp"
	}
	return "Stomp (" + strings.Join(keys, ", ") + ")"
}

func extractPresetName(chunk string) string {
	re := regexp.MustCompile(`PresetName="([^"]+)"`)
	if m := re.FindStringSubmatch(chunk); len(m) > 1 {
		return m[1]
	}
	return ""
}

func applyDiskPreset(liveChunk, presetFilePath, presetName string) (string, error) {
	presetBytes, err := os.ReadFile(presetFilePath)
	if err != nil {
		return "", err
	}
	presetStr := string(presetBytes)

	// Extract inner XML inside <Preset ...>inner</Preset>
	rePreset := regexp.MustCompile(`(?s)<Preset\b[^>]*>([\s\S]*?)</Preset>`)
	m := rePreset.FindStringSubmatch(presetStr)
	if len(m) <= 1 {
		return "", fmt.Errorf("no <Preset> element found in %s", presetFilePath)
	}
	presetInner := strings.TrimSpace(m[1])

	env, _, err := unwrap(1, liveChunk)
	if err != nil {
		return "", err
	}

	// Update live <Program ...> tag with PresetName and PresetPath
	reProg := regexp.MustCompile(`(?s)(<Program\b[^>]*>)([\s\S]*?)(</Program>)`)
	progMatch := reProg.FindStringSubmatchIndex(env.InnerPayload)
	if progMatch == nil {
		return "", fmt.Errorf("no <Program> element found in live chunk")
	}

	progTag := env.InnerPayload[progMatch[2]:progMatch[3]]
	// Update PresetName attribute
	if strings.Contains(progTag, `PresetName="`) {
		reName := regexp.MustCompile(`PresetName="[^"]*"`)
		progTag = reName.ReplaceAllString(progTag, fmt.Sprintf(`PresetName="%s"`, presetName))
	} else {
		progTag = strings.TrimSuffix(progTag, ">") + fmt.Sprintf(` PresetName="%s">`, presetName)
	}

	// Update PresetPath attribute
	if strings.Contains(progTag, `PresetPath="`) {
		rePath := regexp.MustCompile(`PresetPath="[^"]*"`)
		progTag = rePath.ReplaceAllString(progTag, fmt.Sprintf(`PresetPath="%s"`, presetFilePath))
	} else {
		progTag = strings.TrimSuffix(progTag, ">") + fmt.Sprintf(` PresetPath="%s">`, presetFilePath)
	}

	updatedInner := env.InnerPayload[:progMatch[2]] + progTag + "\n" + presetInner + "\n" + env.InnerPayload[progMatch[6]:]
	return wrapPayload(env, updatedInner)
}

func swapPluginModel(liveChunk, slot, model string, dirs []string) (string, error) {
	cat := loadModelCatalog(dirs)
	env, _, err := unwrap(1, liveChunk)
	if err != nil {
		return "", err
	}

	updatedInner := env.InnerPayload

	if strings.HasPrefix(slot, "Amp") {
		// AmpA, AmpB, AmpC
		tmpl, ok := cat.ampTemplates[model]
		if !ok {
			return "", fmt.Errorf("amp model %q not found in catalog", model)
		}
		ampTag := slot
		reAmp := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(ampTag) + `\b[^>]*>([\s\S]*?)</` + regexp.QuoteMeta(ampTag) + `>`)
		if !reAmp.MatchString(updatedInner) {
			return "", fmt.Errorf("slot %q not found in live chunk", slot)
		}
		newAmpBlock := fmt.Sprintf("<%s Bypass=\"0\" Mute=\"0\" OutputVolume=\"1\" Model=\"%s\">\n%s\n</%s>",
			ampTag, tmpl.ModelUUID, tmpl.InnerXML, ampTag)
		updatedInner = reAmp.ReplaceAllString(updatedInner, newAmpBlock)

	} else if strings.HasPrefix(slot, "Cab") {
		tmpl, ok := cat.cabTemplates[model]
		if !ok {
			return "", fmt.Errorf("cab model %q not found in catalog", model)
		}
		cabTag := slot
		reCab := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(cabTag) + `\b[^>]*>([\s\S]*?)</` + regexp.QuoteMeta(cabTag) + `>`)
		if !reCab.MatchString(updatedInner) {
			return "", fmt.Errorf("slot %q not found in live chunk", slot)
		}
		newCabBlock := fmt.Sprintf("<%s Bypass=\"0\" Mute=\"0\" CabModel=\"%s\">\n%s\n</%s>",
			cabTag, tmpl.CabModelUUID, tmpl.InnerXML, cabTag)
		updatedInner = reCab.ReplaceAllString(updatedInner, newCabBlock)

	} else if strings.Contains(slot, ".") {
		// Stomp/rack slot e.g. StompA1.Slot0
		parts := strings.Split(slot, ".")
		bankTag, slotTag := parts[0], parts[1]
		slotNum := strings.TrimPrefix(slotTag, "Slot")
		stompAttr := "Stomp" + slotNum

		tmpl, ok := cat.stompTemplates[model]
		if !ok {
			return "", fmt.Errorf("stomp model %q not found in catalog", model)
		}

		reBank := regexp.MustCompile(`(?s)(<` + regexp.QuoteMeta(bankTag) + `\b[^>]*>)([\s\S]*?)(</` + regexp.QuoteMeta(bankTag) + `>)`)
		m := reBank.FindStringSubmatchIndex(updatedInner)
		if m == nil {
			return "", fmt.Errorf("bank %q not found in live chunk", bankTag)
		}

		bankTagStr := updatedInner[m[2]:m[3]]
		bankBody := updatedInner[m[4]:m[5]]

		// Update StompN="UUID" in bank opening tag
		reAttr := regexp.MustCompile(stompAttr + `="[^"]*"`)
		if reAttr.MatchString(bankTagStr) {
			bankTagStr = reAttr.ReplaceAllString(bankTagStr, fmt.Sprintf(`%s="%s"`, stompAttr, tmpl.StompUUID))
		} else {
			bankTagStr = strings.TrimSuffix(bankTagStr, ">") + fmt.Sprintf(` %s="%s">`, stompAttr, tmpl.StompUUID)
		}

		// Update SlotN in bank body
		var newSlotStr string
		if len(tmpl.SlotAttrs) == 0 {
			newSlotStr = fmt.Sprintf("<%s />", slotTag)
		} else {
			var attrPairs []string
			for k, v := range tmpl.SlotAttrs {
				attrPairs = append(attrPairs, fmt.Sprintf(`%s="%s"`, k, v))
			}
			sort.Strings(attrPairs)
			newSlotStr = fmt.Sprintf("<%s Bypass=\"0\" %s />", slotTag, strings.Join(attrPairs, " "))
		}

		reSlot := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(slotTag) + `\b[^>]*/>|<` + regexp.QuoteMeta(slotTag) + `\b[^>]*>[\s\S]*?</` + regexp.QuoteMeta(slotTag) + `>`)
		if reSlot.MatchString(bankBody) {
			bankBody = reSlot.ReplaceAllString(bankBody, newSlotStr)
		} else {
			bankBody = bankBody + "\n" + newSlotStr
		}

		updatedInner = updatedInner[:m[2]] + bankTagStr + bankBody + updatedInner[m[6]:]
	} else {
		return "", fmt.Errorf("unsupported slot format %q", slot)
	}

	return wrapPayload(env, updatedInner)
}

// PluginModels lists modular slots and dynamically discovered models for an effect.
func (b Bridge) PluginModels(ctx context.Context, track, fx int, slotQuery string) ([]core.PluginSlot, error) {
	chunk, err := b.GetChunk(ctx, track, fx)
	if err != nil {
		return nil, err
	}
	dirs := findPresetDirs(chunk, b.PresetDirs)
	cat := loadModelCatalog(dirs)

	env, _, err := unwrap(fx, chunk)
	if err != nil {
		return nil, err
	}

	var slots []core.PluginSlot

	// Discover slots present in env.InnerPayload
	discoverSlots := func() {
		// Amp slots
		for _, amp := range []string{"AmpA", "AmpB", "AmpC"} {
			if strings.Contains(env.InnerPayload, "<"+amp) {
				current := "Unknown"
				reModel := regexp.MustCompile(`(?s)<` + amp + `\b[^>]*\bModel="([^"]+)"[^>]*>[\s\S]*?<Amp\b([^>]*)/?>`)
				if m := reModel.FindStringSubmatch(env.InnerPayload); len(m) > 2 {
					reSuffix := regexp.MustCompile(`_([A-Za-z0-9]+)="`)
					for _, sm := range reSuffix.FindAllStringSubmatch(m[2], -1) {
						if !strings.EqualFold(sm[1], "Level") && !strings.EqualFold(sm[1], "Type") && len(sm[1]) >= 3 {
							current = sm[1]
							break
						}
					}
					if current == "Unknown" {
						current = m[1]
					}
				}
				var avail []string
				for name := range cat.ampTemplates {
					avail = append(avail, name)
				}
				sort.Strings(avail)
				slots = append(slots, core.PluginSlot{Slot: amp, Current: current, Available: avail})
			}
		}

		// Cab slots
		for _, cab := range []string{"CabA", "CabB", "CabC"} {
			if strings.Contains(env.InnerPayload, "<"+cab) {
				current := "Default"
				reRoom := regexp.MustCompile(`(?s)<` + cab + `\b[\s\S]*?RoomType="([^"]+)"`)
				if rm := reRoom.FindStringSubmatch(env.InnerPayload); len(rm) > 1 {
					current = rm[1]
				}
				var avail []string
				for name := range cat.cabTemplates {
					avail = append(avail, name)
				}
				sort.Strings(avail)
				slots = append(slots, core.PluginSlot{Slot: cab, Current: current, Available: avail})
			}
		}

		// Stomp slots
		reBankOpen := regexp.MustCompile(`<((?:Stomp|LoopFx|Rack)[A-Za-z0-9]+)\b([^>]*)>`)
		bankMatches := reBankOpen.FindAllStringSubmatchIndex(env.InnerPayload, -1)
		for _, bm := range bankMatches {
			bankName := env.InnerPayload[bm[2]:bm[3]]
			bankAttrs := env.InnerPayload[bm[4]:bm[5]]
			closeTag := "</" + bankName + ">"
			rest := env.InnerPayload[bm[1]:]
			closeIdx := strings.Index(rest, closeTag)
			if closeIdx < 0 {
				continue
			}
			bankBody := rest[:closeIdx]

			maxSlots := 6
			if strings.HasPrefix(bankName, "LoopFx") || strings.HasPrefix(bankName, "Rack") {
				maxSlots = 4
			}

			for s := 0; s < maxSlots; s++ {
				slotName := fmt.Sprintf("%s.Slot%d", bankName, s)
				stompAttr := fmt.Sprintf("Stomp%d", s)
				slotTag := fmt.Sprintf("Slot%d", s)

				current := "Empty"
				reUUID := regexp.MustCompile(stompAttr + `="([^"]+)"`)
				if m := reUUID.FindStringSubmatch(bankAttrs); len(m) > 1 && m[1] != "" && m[1] != "773b8ea7-b54a-4a3c-99df-ffbbf6d29271" {
					reSlot := regexp.MustCompile(`(?s)<` + slotTag + `\b([^>]*)/?>`)
					if sm := reSlot.FindStringSubmatch(bankBody); len(sm) > 1 {
						attrs := map[string]string{}
						for _, am := range regexp.MustCompile(`([A-Za-z0-9_]+)="([^"]*)"`).FindAllStringSubmatch(sm[1], -1) {
							attrs[am[1]] = am[2]
						}
						current = identifyStompName(attrs)
					}
				}

				var avail []string
				for name := range cat.stompTemplates {
					avail = append(avail, name)
				}
				sort.Strings(avail)
				slots = append(slots, core.PluginSlot{Slot: slotName, Current: current, Available: avail})
			}
		}
	}

	discoverSlots()

	// Cap Available list per slot to at most 40 entries sorted alphabetically
	for i := range slots {
		sort.Strings(slots[i].Available)
		if len(slots[i].Available) > 40 {
			slots[i].Available = slots[i].Available[:40]
		}
	}

	if slotQuery == "" {
		var compact []core.PluginSlot
		isPrimary := func(name string) bool {
			switch name {
			case "AmpA", "CabA", "StompA1.Slot0", "StompA1.Slot1", "RackA.Slot0":
				return true
			}
			return false
		}
		for _, s := range slots {
			if isPrimary(s.Slot) || (s.Current != "Empty" && s.Current != "Unknown" && s.Current != "" && s.Current != "Default") {
				compact = append(compact, s)
			}
		}
		return compact, nil
	}

	var filtered []core.PluginSlot
	q := strings.ToLower(slotQuery)
	for _, s := range slots {
		if strings.Contains(strings.ToLower(s.Slot), q) {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
}

// SetPluginModel swaps an internal sub-model in a plugin slot and returns the updated parameters.
func (b Bridge) SetPluginModel(ctx context.Context, track, fx int, slot, model string) ([]core.Param, error) {
	chunk, err := b.GetChunk(ctx, track, fx)
	if err != nil {
		return nil, err
	}
	dirs := findPresetDirs(chunk, b.PresetDirs)
	updatedChunk, err := swapPluginModel(chunk, slot, model, dirs)
	if err != nil {
		return nil, err
	}
	// Read back what the plugin kept: swapping again must change nothing,
	// or the plugin refused the model and reverted.
	if err := b.keepWrite(ctx, track, fx, updatedChunk, func(back string) error {
		if again, err := swapPluginModel(back, slot, model, dirs); err == nil && again != back {
			return core.ReadOnlyError{FX: fx, Detail: fmt.Sprintf("the plugin did not keep %s in %s", model, slot)}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	back, err := b.GetChunk(ctx, track, fx)
	if err != nil {
		return nil, err
	}
	return ParseChunk(fx, back)
}

var _ core.ModelSwapper = Bridge{}
