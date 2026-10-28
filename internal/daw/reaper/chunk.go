package reaper

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type scaleKind int

const (
	scaleKnob10 scaleKind = iota
	scaleDB40
	scaleNorm
	scaleSwitch
	scaleDiscrete
)

type projectedParamInfo struct {
	Param      core.Param
	Format     string // "xml", "json", "kv"
	Scale      scaleKind
	ElemTag    string
	ElemParent string
	ElemIndex  int
	AttrName   string
	OrigValue  string
	JSONPath   []string
	KVKey      string
}

type unwrapEnvelope struct {
	HasReaperWrapper  bool
	ReaperHeader      string
	ReaperFooter      string
	ReaperIndent      string
	ReaperFooterLines []string

	OuterBase64 bool

	HasPlist          bool
	PlistXML          string
	PlistKey          string // "jucePluginState" or "data"
	PlistPrefix       string
	PlistSuffix       string
	PlistBinaryPrefix []byte
	PlistBinarySuffix []byte
	OrigPlistLen      int

	Compression string // "", "gzip", "zlib"

	BinaryPrefix []byte
	BinarySuffix []byte
	OrigInnerLen int

	InnerPayload string
	InnerFormat  string // "xml", "json", "kv"
}

var uuidRegex = regexp.MustCompile(`(?i)^\{?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}?$`)

func isUUID(s string) bool {
	s = strings.TrimSpace(s)
	return uuidRegex.MatchString(s)
}

func isNoiseAttr(name, val string) bool {
	val = strings.TrimSpace(val)
	if val == "" || isUUID(val) {
		return true
	}
	lower := strings.ToLower(name)
	switch lower {
	case "guid", "version", "format", "presetpath", "presetname", "programchange",
		"presetbpm", "guiloadcomplete", "irdecimation", "cabmodel", "model", "tunertype":
		return true
	}
	if strings.HasSuffix(lower, "guid") {
		return true
	}
	if strings.HasPrefix(lower, "speakermodel") {
		return true
	}
	if strings.HasPrefix(lower, "mic") && strings.Contains(lower, "model") {
		return true
	}
	if strings.HasPrefix(lower, "stomp") && (isUUID(val) || val == "0" || val == "") {
		return true
	}
	return false
}

func cleanAttributeName(elemName, attrName, model string) string {
	cleanAttr := attrName
	if model != "" && strings.HasSuffix(cleanAttr, "_"+model) {
		cleanAttr = strings.TrimSuffix(cleanAttr, "_"+model)
	} else if idx := strings.LastIndex(cleanAttr, "_"); idx > 0 {
		suffix := cleanAttr[idx+1:]
		hasUpperOrDigit := false
		for _, r := range suffix {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				hasUpperOrDigit = true
				break
			}
		}
		if hasUpperOrDigit && len(suffix) >= 3 && !strings.EqualFold(suffix, "Level") && !strings.EqualFold(suffix, "Type") && !strings.EqualFold(suffix, "Mode") {
			cleanAttr = cleanAttr[:idx]
		}
	}
	if elemName != "" && !strings.EqualFold(elemName, "Program") && !strings.EqualFold(elemName, "Preset") && !strings.EqualFold(elemName, "ValueTree") {
		return elemName + " " + cleanAttr
	}
	return cleanAttr
}

func normalizeParam(name, rawVal string) (norm float64, kind string, scale scaleKind) {
	raw := strings.TrimSpace(rawVal)
	lowerName := strings.ToLower(name)
	isSwitchName := strings.HasSuffix(lowerName, "bypass") || strings.HasSuffix(lowerName, "mute") ||
		strings.HasSuffix(lowerName, "enable") || strings.HasSuffix(lowerName, "enabled") ||
		strings.HasSuffix(lowerName, "active") || strings.HasSuffix(lowerName, "on")

	if (raw == "0" || raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false")) &&
		(isSwitchName || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false")) {
		kind = "switch"
		scale = scaleSwitch
		if raw == "1" || strings.EqualFold(raw, "true") {
			norm = 1.0
		} else {
			norm = 0.0
		}
		return norm, kind, scale
	}

	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		kind = "discrete"
		scale = scaleDiscrete
		norm = 0.0
		return norm, kind, scale
	}

	kind = ""
	if val < 0 || (strings.Contains(lowerName, "level") && val <= 0) {
		scale = scaleDB40
		norm = (val + 40.0) / 40.0
		if norm < 0 {
			norm = 0
		} else if norm > 1 {
			norm = 1
		}
		return norm, kind, scale
	}

	isKnobName := strings.Contains(lowerName, "preamp") || strings.Contains(lowerName, "gain") ||
		strings.Contains(lowerName, "drive") || strings.Contains(lowerName, "bass") ||
		strings.Contains(lowerName, "middle") || strings.Contains(lowerName, "treble") ||
		strings.Contains(lowerName, "presence") || strings.Contains(lowerName, "master") ||
		strings.Contains(lowerName, "volume") || strings.Contains(lowerName, "sensitivity") ||
		strings.Contains(lowerName, "tone")
	if val > 1.0 || (isKnobName && val >= 0.0 && val <= 10.0) {
		scale = scaleKnob10
		norm = val / 10.0
		if norm < 0 {
			norm = 0
		} else if norm > 1 {
			norm = 1
		}
		return norm, kind, scale
	}

	scale = scaleNorm
	norm = val
	if norm < 0 {
		norm = 0
	} else if norm > 1 {
		norm = 1
	}
	return norm, kind, scale
}

func formatPatchedValue(norm float64, scale scaleKind, origText string) (newText string, confirmedNorm float64, readout string) {
	if norm < 0 {
		norm = 0
	} else if norm > 1 {
		norm = 1
	}
	switch scale {
	case scaleSwitch:
		if norm >= 0.5 {
			confirmedNorm = 1.0
			if origText == "true" || origText == "false" {
				newText = "true"
			} else {
				newText = "1"
			}
		} else {
			confirmedNorm = 0.0
			if origText == "true" || origText == "false" {
				newText = "false"
			} else {
				newText = "0"
			}
		}
		readout = newText
	case scaleKnob10:
		val := norm * 10.0
		confirmedNorm = math.Round(val*100) / 1000
		newText = fmt.Sprintf("%.2f", val)
		readout = newText
	case scaleDB40:
		val := -40.0 + norm*40.0
		confirmedNorm = math.Round(((val+40.0)/40.0)*1000) / 1000
		newText = fmt.Sprintf("%.1f", val)
		readout = newText + " dB"
	case scaleNorm:
		confirmedNorm = math.Round(norm*1000) / 1000
		newText = fmt.Sprintf("%.3f", norm)
		readout = newText
	case scaleDiscrete:
		confirmedNorm = 0.0
		newText = origText
		readout = origText
	}
	return newText, confirmedNorm, readout
}

func juceCompressedInt(n int) []byte {
	if n <= 0xff {
		return []byte{1, byte(n)}
	} else if n <= 0xffff {
		return []byte{2, byte(n), byte(n >> 8)}
	} else if n <= 0xffffff {
		return []byte{3, byte(n), byte(n >> 8), byte(n >> 16)}
	}
	return []byte{4, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
}

// unwrap takes any raw chunk container and produces the unwrap envelope
// along with the parsed projected parameters.
func unwrap(fxIndex int, rawChunk string) (*unwrapEnvelope, []projectedParamInfo, error) {
	env := &unwrapEnvelope{}
	cur := strings.TrimSpace(rawChunk)
	var payload []byte

	// 1. REAPER chunk wrapper (<AU ...>, <VST ...>, <JS ...>, <CLAP ...>)
	if strings.HasPrefix(cur, "<") {
		firstLineEnd := strings.IndexByte(cur, '\n')
		if firstLineEnd > 0 {
			firstLine := strings.TrimRight(cur[:firstLineEnd], "\r")
			tag := strings.TrimLeft(firstLine, "<")
			tag = strings.Fields(tag)[0]
			switch strings.ToUpper(tag) {
			case "AU", "VST", "JS", "CLAP", "FXCHUNK":
				env.HasReaperWrapper = true
				env.ReaperHeader = firstLine
				env.ReaperFooter = ">"

				rest := cur[firstLineEnd+1:]
				rawLines := strings.Split(rest, "\n")
				var mainLines []string
				var footerLines []string
				foundClosing := false

				for _, line := range rawLines {
					trimmed := strings.TrimSpace(line)
					if !foundClosing {
						if trimmed == ">" {
							foundClosing = true
							env.ReaperFooter = strings.TrimRight(line, "\r")
							continue
						}
						if strings.HasPrefix(trimmed, "FLOATPOS ") || strings.HasPrefix(trimmed, "FXID ") ||
							strings.HasPrefix(trimmed, "WAK ") || strings.HasPrefix(trimmed, "BYPASS ") {
							footerLines = append(footerLines, strings.TrimRight(line, "\r"))
							continue
						}
						mainLines = append(mainLines, line)
					} else {
						if trimmed != "" {
							footerLines = append(footerLines, strings.TrimRight(line, "\r"))
						}
					}
				}
				env.ReaperFooterLines = footerLines

				// Detect indent of body lines
				for _, line := range mainLines {
					if len(strings.TrimSpace(line)) > 0 {
						indentLen := len(line) - len(strings.TrimLeft(line, " \t"))
						if indentLen > 0 {
							env.ReaperIndent = line[:indentLen]
						}
						break
					}
				}
				if env.ReaperIndent == "" {
					env.ReaperIndent = "  "
				}

				cleanB64 := strings.Map(func(r rune) rune {
					if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
						return -1
					}
					return r
				}, strings.Join(mainLines, ""))

				if dec, err := base64.StdEncoding.DecodeString(cleanB64); err == nil {
					payload = dec
				} else {
					payload = []byte(rest)
				}
			}
		}
	}

	if payload == nil {
		cleanB64 := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, cur)
		if dec, err := base64.StdEncoding.DecodeString(cleanB64); err == nil && len(dec) > 8 &&
			(bytes.Contains(dec, []byte("<")) || bytes.Contains(dec, []byte("{")) || dec[0] == 0x1f || dec[0] == 0x78) {
			env.OuterBase64 = true
			payload = dec
		} else {
			payload = []byte(cur)
		}
	}

	// 2. Apple <plist> XML container
	if bytes.Contains(payload, []byte("<plist")) && bytes.Contains(payload, []byte("<dict>")) {
		plistStart := bytes.Index(payload, []byte("<plist"))
		if plistStart >= 0 {
			searchFrom := 0
			if plistStart > 120 {
				searchFrom = plistStart - 120
			}
			if idx := bytes.Index(payload[searchFrom:plistStart], []byte("<?xml")); idx >= 0 {
				plistStart = searchFrom + idx
			}
		}
		closeTag := []byte("</plist>")
		closeIdx := bytes.Index(payload, closeTag)
		if plistStart >= 0 && closeIdx >= 0 {
			plistEnd := closeIdx + len(closeTag)
			env.PlistBinaryPrefix = payload[:plistStart]
			env.PlistBinarySuffix = payload[plistEnd:]
			env.OrigPlistLen = plistEnd - plistStart

			plistStr := string(payload[plistStart:plistEnd])
			for _, key := range []string{"jucePluginState", "data"} {
				pattern := regexp.MustCompile(`(?s)(<key>` + key + `</key>\s*<data>\s*)([A-Za-z0-9+/=\s]+?)(\s*</data>)`)
				if m := pattern.FindStringSubmatchIndex(plistStr); m != nil {
					env.HasPlist = true
					env.PlistKey = key
					env.PlistXML = plistStr
					env.PlistPrefix = plistStr[:m[4]]
					env.PlistSuffix = plistStr[m[5]:]
					b64Data := plistStr[m[4]:m[5]]
					cleanB64 := strings.Map(func(r rune) rune {
						if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
							return -1
						}
						return r
					}, b64Data)
					if dec, err := base64.StdEncoding.DecodeString(cleanB64); err == nil {
						payload = dec
						break
					}
				}
			}
		}
	}

	// 3. Decompression (Gzip 0x1f 0x8b, Zlib 0x78 0x9c / 0x78 0x01 / 0x78 0xda)
	if len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		env.Compression = "gzip"
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err == nil {
			if uncompressed, err := io.ReadAll(gz); err == nil {
				payload = uncompressed
			}
			gz.Close()
		}
	} else if len(payload) >= 2 && payload[0] == 0x78 && (payload[1] == 0x9c || payload[1] == 0x01 || payload[1] == 0xda || payload[1] == 0x5e) {
		env.Compression = "zlib"
		zr, err := zlib.NewReader(bytes.NewReader(payload))
		if err == nil {
			if uncompressed, err := io.ReadAll(zr); err == nil {
				payload = uncompressed
			}
			zr.Close()
		}
	}

	// 4. JUCE binary prefix (e.g. State\x00...PresetData...<?xml)
	markers := [][]byte{[]byte("<?xml"), []byte("<Program"), []byte("<Preset"), []byte("<ValueTree"), []byte("<State")}
	foundXML := -1
	for _, m := range markers {
		if idx := bytes.Index(payload, m); idx >= 0 {
			if foundXML < 0 || idx < foundXML {
				foundXML = idx
			}
		}
	}
	if foundXML >= 0 {
		env.BinaryPrefix = payload[:foundXML]
		xmlBytes := payload[foundXML:]

		// Find the root element name and closing tag
		var rootTag string
		tagSearch := xmlBytes
		if bytes.HasPrefix(tagSearch, []byte("<?xml")) {
			if endDecl := bytes.Index(tagSearch, []byte("?>")); endDecl >= 0 {
				tagSearch = tagSearch[endDecl+2:]
			}
		}
		reRoot := regexp.MustCompile(`<([A-Za-z0-9_]+)`)
		if m := reRoot.FindSubmatch(tagSearch); len(m) > 1 {
			rootTag = string(m[1])
		}
		if rootTag != "" {
			closeTag := []byte("</" + rootTag + ">")
			if idx := bytes.Index(xmlBytes, closeTag); idx >= 0 {
				endOfDoc := idx + len(closeTag)
				env.InnerPayload = string(xmlBytes[:endOfDoc])
				env.BinarySuffix = xmlBytes[endOfDoc:]
				env.OrigInnerLen = len(env.InnerPayload)
			} else {
				env.InnerPayload = string(xmlBytes)
				env.OrigInnerLen = len(env.InnerPayload)
			}
		} else {
			env.InnerPayload = string(xmlBytes)
			env.OrigInnerLen = len(env.InnerPayload)
		}
	} else {
		// Check JSON {
		if idx := bytes.IndexByte(payload, '{'); idx > 0 {
			var testObj map[string]any
			if json.Unmarshal(payload[idx:], &testObj) == nil {
				env.BinaryPrefix = payload[:idx]
				env.InnerPayload = string(payload[idx:])
				env.OrigInnerLen = len(env.InnerPayload)
			}
		}
		if env.InnerPayload == "" {
			env.InnerPayload = string(payload)
			env.OrigInnerLen = len(env.InnerPayload)
		}
	}

	// 5. Determine Inner Payload Format
	trimmedPayload := strings.TrimSpace(env.InnerPayload)
	if strings.HasPrefix(trimmedPayload, "<") {
		env.InnerFormat = "xml"
		params, err := parseXMLPayload(fxIndex, env.InnerPayload)
		return env, params, err
	} else if strings.HasPrefix(trimmedPayload, "{") {
		env.InnerFormat = "json"
		params, err := parseJSONPayload(fxIndex, env.InnerPayload)
		return env, params, err
	}
	env.InnerFormat = "kv"
	params, err := parseKVPayload(fxIndex, env.InnerPayload)
	return env, params, err
}

// wrapPayload reverses the unwrap envelope to encode the updated inner payload back to rawChunk.
func wrapPayload(env *unwrapEnvelope, patchedInner string) (string, error) {
	newPrefix := env.BinaryPrefix
	presetDataMarker := []byte("PresetData\x00")
	if idx := bytes.Index(newPrefix, presetDataMarker); idx >= 0 {
		pos := idx + len(presetDataMarker)
		if len(newPrefix) > pos && newPrefix[len(newPrefix)-1] == 0x08 {
			newInt := len(patchedInner) + 2
			prefixHead := append([]byte{}, newPrefix[:pos]...)
			prefixHead = append(prefixHead, juceCompressedInt(newInt)...)
			prefixHead = append(prefixHead, 0x08)
			newPrefix = prefixHead
		}
	}

	binaryBytes := append(newPrefix, []byte(patchedInner)...)
	if len(env.BinarySuffix) > 0 {
		binaryBytes = append(binaryBytes, env.BinarySuffix...)
	}

	if env.Compression == "gzip" {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(binaryBytes); err != nil {
			return "", err
		}
		if err := gw.Close(); err != nil {
			return "", err
		}
		binaryBytes = buf.Bytes()
	} else if env.Compression == "zlib" {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write(binaryBytes); err != nil {
			return "", err
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
		binaryBytes = buf.Bytes()
	}

	var payloadBytes []byte
	if env.HasPlist {
		b64Inner := base64.StdEncoding.EncodeToString(binaryBytes)
		var b64Lines []string
		for len(b64Inner) > 68 {
			b64Lines = append(b64Lines, "\t\t"+b64Inner[:68])
			b64Inner = b64Inner[68:]
		}
		if len(b64Inner) > 0 {
			b64Lines = append(b64Lines, "\t\t"+b64Inner)
		}
		formattedData := "\n" + strings.Join(b64Lines, "\n") + "\n\t"
		pattern := regexp.MustCompile(`(?s)(<key>` + env.PlistKey + `</key>\s*<data>\s*)([A-Za-z0-9+/=\s]+?)(\s*</data>)`)
		updatedPlist := pattern.ReplaceAllString(env.PlistXML, `${1}`+formattedData+`${3}`)

		if len(env.PlistBinaryPrefix) >= 4 {
			prefix := make([]byte, len(env.PlistBinaryPrefix))
			copy(prefix, env.PlistBinaryPrefix)
			last4 := binary.LittleEndian.Uint32(prefix[len(prefix)-4:])
			if int(last4) >= env.OrigPlistLen && int(last4) <= env.OrigPlistLen+16 {
				diff := len(updatedPlist) - env.OrigPlistLen
				binary.LittleEndian.PutUint32(prefix[len(prefix)-4:], uint32(int(last4)+diff))
			}
			payloadBytes = append(prefix, []byte(updatedPlist)...)
			payloadBytes = append(payloadBytes, env.PlistBinarySuffix...)
		} else {
			payloadBytes = []byte(updatedPlist)
		}
	} else {
		payloadBytes = binaryBytes
	}

	if env.HasReaperWrapper {
		b64Outer := base64.StdEncoding.EncodeToString(payloadBytes)
		var b64Lines []string
		for len(b64Outer) > 72 {
			b64Lines = append(b64Lines, env.ReaperIndent+b64Outer[:72])
			b64Outer = b64Outer[72:]
		}
		if len(b64Outer) > 0 {
			b64Lines = append(b64Lines, env.ReaperIndent+b64Outer)
		}
		var footerStr string
		if len(env.ReaperFooterLines) > 0 {
			footerStr = "\n" + strings.Join(env.ReaperFooterLines, "\n")
		}
		return env.ReaperHeader + "\n" + strings.Join(b64Lines, "\n") + "\n" + env.ReaperFooter + footerStr + "\n", nil
	}

	if env.OuterBase64 {
		return base64.StdEncoding.EncodeToString(payloadBytes), nil
	}

	return string(payloadBytes), nil
}

func parseXMLPayload(fxIndex int, xmlStr string) ([]projectedParamInfo, error) {
	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	var out []projectedParamInfo
	elemOccurrences := map[string]int{}
	var stack []string
	var skipDepth int

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			stack = append(stack, name)
			if skipDepth > 0 {
				continue
			}

			lowerName := strings.ToLower(name)
			// Only skip slot when it has NO attributes
			if strings.HasPrefix(lowerName, "slot") && len(t.Attr) == 0 {
				skipDepth = len(stack)
				continue
			}
			if lowerName == "midiassignments" || lowerName == "preferences" ||
				lowerName == "automation" || lowerName == "panels" ||
				lowerName == "guiscale" || lowerName == "gui" || lowerName == "midi" {
				skipDepth = len(stack)
				continue
			}

			occ := elemOccurrences[name]
			elemOccurrences[name]++

			// Check attributes
			attrMap := map[string]string{}
			for _, a := range t.Attr {
				attrMap[a.Name.Local] = a.Value
			}

			// If element has Bypass="1" and has no active model, skip it if it has no musical params
			if attrMap["Bypass"] == "1" {
				model := attrMap["Model"]
				if model == "" {
					hasParams := false
					for _, a := range t.Attr {
						if a.Name.Local != "Bypass" && a.Name.Local != "Model" && !isNoiseAttr(a.Name.Local, a.Value) {
							hasParams = true
							break
						}
					}
					if !hasParams {
						skipDepth = len(stack)
						continue
					}
				}
			}

			parent := ""
			if len(stack) >= 2 {
				parent = stack[len(stack)-2]
			}
			labelName := name
			if strings.HasPrefix(lowerName, "slot") && parent != "" {
				labelName = parent + " " + name
			} else if (lowerName == "amp" || lowerName == "cab") && parent != "" {
				labelName = parent
			}

			model := attrMap["Model"]
			for _, a := range t.Attr {
				attrName := a.Name.Local
				val := a.Value
				if isNoiseAttr(attrName, val) {
					continue
				}

				cleanedName := cleanAttributeName(labelName, attrName, model)
				norm, kind, scale := normalizeParam(cleanedName, val)
				p := core.Param{
					ID:    core.MakeParamID(fxIndex, 1000+len(out)+1),
					Name:  cleanedName,
					Kind:  kind,
					Text:  val,
					Value: norm,
				}
				out = append(out, projectedParamInfo{
					Param:      p,
					Format:     "xml",
					Scale:      scale,
					ElemTag:    name,
					ElemParent: parent,
					ElemIndex:  occ,
					AttrName:   attrName,
					OrigValue:  val,
				})
			}

		case xml.EndElement:
			if skipDepth > 0 && len(stack) == skipDepth {
				skipDepth = 0
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return out, nil
}

func patchXMLPayload(xmlStr string, info projectedParamInfo, newVal string) (string, error) {
	if info.ElemParent != "" {
		parentRe := regexp.MustCompile(`(?s)(<` + regexp.QuoteMeta(info.ElemParent) + `\b[^>]*>)([\s\S]*?)(</` + regexp.QuoteMeta(info.ElemParent) + `>)`)
		parentIndices := parentRe.FindAllStringSubmatchIndex(xmlStr, -1)
		if len(parentIndices) > 0 {
			for _, pIdx := range parentIndices {
				parentInner := xmlStr[pIdx[4]:pIdx[5]]
				tagRe := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(info.ElemTag) + `(\s+[^>]*)?>`)
				tagIndices := tagRe.FindAllStringIndex(parentInner, -1)
				if len(tagIndices) > 0 {
					tIdx := tagIndices[0]
					tagContent := parentInner[tIdx[0]:tIdx[1]]
					attrRe := regexp.MustCompile(`(` + regexp.QuoteMeta(info.AttrName) + `\s*=\s*["'])([^"']*)(["'])`)
					if attrRe.MatchString(tagContent) {
						updatedTag := attrRe.ReplaceAllString(tagContent, `${1}`+newVal+`${3}`)
						updatedInner := parentInner[:tIdx[0]] + updatedTag + parentInner[tIdx[1]:]
						return xmlStr[:pIdx[4]] + updatedInner + xmlStr[pIdx[5]:], nil
					}
				}
			}
		}
	}

	tagRe := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(info.ElemTag) + `(\s+[^>]*)?>`)
	indices := tagRe.FindAllStringIndex(xmlStr, -1)
	if len(indices) <= info.ElemIndex {
		return "", fmt.Errorf("element %q occurrence %d not found in XML", info.ElemTag, info.ElemIndex)
	}

	matchIdx := indices[info.ElemIndex]
	tagContent := xmlStr[matchIdx[0]:matchIdx[1]]

	attrRe := regexp.MustCompile(`(` + regexp.QuoteMeta(info.AttrName) + `\s*=\s*["'])([^"']*)(["'])`)
	if !attrRe.MatchString(tagContent) {
		return "", fmt.Errorf("attribute %q not found in element %q", info.AttrName, info.ElemTag)
	}

	updatedTag := attrRe.ReplaceAllString(tagContent, `${1}`+newVal+`${3}`)
	return xmlStr[:matchIdx[0]] + updatedTag + xmlStr[matchIdx[1]:], nil
}

func parseJSONPayload(fxIndex int, jsonStr string) ([]projectedParamInfo, error) {
	var root any
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return nil, err
	}
	var out []projectedParamInfo

	var walk func(val any, path []string)
	walk = func(val any, path []string) {
		switch v := val.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v[k], append(path, k))
			}
		case []any:
			for i, child := range v {
				walk(child, append(path, strconv.Itoa(i)))
			}
		case float64:
			key := path[len(path)-1]
			if isNoiseAttr(key, fmt.Sprintf("%g", v)) {
				return
			}
			name := strings.Join(path, " ")
			valStr := fmt.Sprintf("%g", v)
			norm, kind, scale := normalizeParam(name, valStr)
			p := core.Param{
				ID:    core.MakeParamID(fxIndex, 1000+len(out)+1),
				Name:  name,
				Kind:  kind,
				Text:  valStr,
				Value: norm,
			}
			out = append(out, projectedParamInfo{
				Param:     p,
				Format:    "json",
				Scale:     scale,
				JSONPath:  path,
				OrigValue: valStr,
			})
		case bool:
			key := path[len(path)-1]
			valStr := strconv.FormatBool(v)
			if isNoiseAttr(key, valStr) {
				return
			}
			name := strings.Join(path, " ")
			norm, kind, scale := normalizeParam(name, valStr)
			p := core.Param{
				ID:    core.MakeParamID(fxIndex, 1000+len(out)+1),
				Name:  name,
				Kind:  kind,
				Text:  valStr,
				Value: norm,
			}
			out = append(out, projectedParamInfo{
				Param:     p,
				Format:    "json",
				Scale:     scale,
				JSONPath:  path,
				OrigValue: valStr,
			})
		case string:
			key := path[len(path)-1]
			if isNoiseAttr(key, v) || len(v) > 60 || strings.Contains(v, "/") || strings.Contains(v, "\\") {
				return
			}
			name := strings.Join(path, " ")
			norm, kind, scale := normalizeParam(name, v)
			p := core.Param{
				ID:    core.MakeParamID(fxIndex, 1000+len(out)+1),
				Name:  name,
				Kind:  kind,
				Text:  v,
				Value: norm,
			}
			out = append(out, projectedParamInfo{
				Param:     p,
				Format:    "json",
				Scale:     scale,
				JSONPath:  path,
				OrigValue: v,
			})
		}
	}

	walk(root, nil)
	return out, nil
}

func patchJSONPayload(jsonStr string, info projectedParamInfo, normVal float64) (string, error) {
	var root any
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return "", err
	}

	newText, _, _ := formatPatchedValue(normVal, info.Scale, info.OrigValue)

	cur := root
	for i := 0; i < len(info.JSONPath)-1; i++ {
		k := info.JSONPath[i]
		m, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("navigating JSON path %v", info.JSONPath)
		}
		cur = m[k]
	}

	last := info.JSONPath[len(info.JSONPath)-1]
	m, ok := cur.(map[string]any)
	if !ok {
		return "", fmt.Errorf("JSON leaf %q not in object", last)
	}

	switch info.Scale {
	case scaleSwitch:
		m[last] = normVal >= 0.5
	case scaleKnob10:
		f, _ := strconv.ParseFloat(newText, 64)
		m[last] = f
	case scaleDB40, scaleNorm:
		f, _ := strconv.ParseFloat(newText, 64)
		m[last] = f
	default:
		m[last] = newText
	}

	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseKVPayload(fxIndex int, kvStr string) ([]projectedParamInfo, error) {
	var out []projectedParamInfo
	for _, line := range strings.Split(kvStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if isNoiseAttr(k, v) || len(v) > 60 || isUUID(v) {
			continue
		}
		norm, kind, scale := normalizeParam(k, v)
		p := core.Param{
			ID:    core.MakeParamID(fxIndex, 1000+len(out)+1),
			Name:  k,
			Kind:  kind,
			Text:  v,
			Value: norm,
		}
		out = append(out, projectedParamInfo{
			Param:     p,
			Format:    "kv",
			Scale:     scale,
			KVKey:     k,
			OrigValue: v,
		})
	}
	return out, nil
}

func patchKVPayload(kvStr string, info projectedParamInfo, newVal string) (string, error) {
	lines := strings.Split(kvStr, "\n")
	found := false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, info.KVKey+"=") || strings.HasPrefix(trimmed, info.KVKey+" =") {
			lines[i] = info.KVKey + "=" + newVal
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("key %q not found in KV payload", info.KVKey)
	}
	return strings.Join(lines, "\n"), nil
}

var discreteTargets = struct {
	sync.Mutex
	m map[string]string // key: "fx:paramOffset" -> target string
}{m: map[string]string{}}

// RegisterDiscreteTarget registers a desired target string for a discrete parameter
// and returns a sentinel normalized value (0.5).
func RegisterDiscreteTarget(fxIndex, paramOffset int, target string) float64 {
	key := fmt.Sprintf("%d:%d", fxIndex, paramOffset)
	discreteTargets.Lock()
	discreteTargets.m[key] = target
	discreteTargets.Unlock()
	return 0.5
}

func getAndClearDiscreteTarget(fxIndex, paramOffset int) (string, bool) {
	discreteTargets.Lock()
	defer discreteTargets.Unlock()
	key := fmt.Sprintf("%d:%d", fxIndex, paramOffset)
	if val, ok := discreteTargets.m[key]; ok {
		delete(discreteTargets.m, key)
		return val, true
	}
	key0 := fmt.Sprintf("0:%d", paramOffset)
	if val, ok := discreteTargets.m[key0]; ok {
		delete(discreteTargets.m, key0)
		return val, true
	}
	return "", false
}

// FindChunkValue calculates the normalized 0..1 value for target on a chunk parameter
// (at 1-based paramOffset = p - 1000) in memory in 0ms without contacting the DAW.
func FindChunkValue(rawChunk string, paramOffset int, target string) (float64, error) {
	_, infos, err := unwrap(1, rawChunk)
	if err != nil {
		return 0, err
	}
	if paramOffset < 1 || paramOffset > len(infos) {
		return 0, fmt.Errorf("param offset %d out of range (1..%d)", paramOffset, len(infos))
	}
	info := infos[paramOffset-1]
	t := strings.TrimSpace(target)

	switch info.Scale {
	case scaleSwitch:
		lower := strings.ToLower(t)
		switch lower {
		case "on", "1", "true", "yes", "active", "enabled", "normal":
			return 1.0, nil
		case "off", "0", "false", "no", "inactive", "disabled", "bypass", "bypassed":
			return 0.0, nil
		}
		if num, _, _, err := parseTargetNumber(t); err == nil {
			if num >= 0.5 {
				return 1.0, nil
			}
			return 0.0, nil
		}
		return 0.0, fmt.Errorf("cannot parse switch target %q", target)

	case scaleKnob10:
		num, isPercent, _, err := parseTargetNumber(t)
		if err != nil {
			return 0, fmt.Errorf("cannot parse knob target %q: %w", target, err)
		}
		var norm float64
		if isPercent || num > 10.0 {
			norm = num / 100.0
		} else {
			norm = num / 10.0
		}
		if norm < 0 {
			norm = 0
		} else if norm > 1 {
			norm = 1
		}
		return norm, nil

	case scaleDB40:
		num, _, _, err := parseTargetNumber(t)
		if err != nil {
			return 0, fmt.Errorf("cannot parse dB target %q: %w", target, err)
		}
		norm := (num + 40.0) / 40.0
		if norm < 0 {
			norm = 0
		} else if norm > 1 {
			norm = 1
		}
		return norm, nil

	case scaleNorm:
		num, isPercent, _, err := parseTargetNumber(t)
		if err != nil {
			return 0, fmt.Errorf("cannot parse norm target %q: %w", target, err)
		}
		var norm float64
		if isPercent || num > 1.0 {
			norm = num / 100.0
		} else {
			norm = num
		}
		if norm < 0 {
			norm = 0
		} else if norm > 1 {
			norm = 1
		}
		return norm, nil

	case scaleDiscrete:
		sentinel := RegisterDiscreteTarget(0, paramOffset, t)
		return sentinel, nil

	default:
		return 0, fmt.Errorf("unsupported scale for target %q", target)
	}
}

func parseTargetNumber(s string) (num float64, isPercent bool, hasDB bool, err error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(t, "%") {
		isPercent = true
		t = strings.TrimSpace(strings.TrimSuffix(t, "%"))
	}
	if strings.HasSuffix(t, "db") {
		hasDB = true
		t = strings.TrimSpace(strings.TrimSuffix(t, "db"))
	}
	t = strings.TrimSpace(strings.TrimPrefix(t, "+"))
	fields := strings.Fields(t)
	if len(fields) > 0 {
		t = fields[0]
	}
	num, err = strconv.ParseFloat(t, 64)
	return num, isPercent, hasDB, err
}

// ParseChunk unwraps multi-format state chunks and projects active parameters.
func ParseChunk(fxIndex int, rawChunk string) ([]core.Param, error) {
	_, infos, err := unwrap(fxIndex, rawChunk)
	if err != nil {
		return nil, err
	}
	out := make([]core.Param, len(infos))
	for i, info := range infos {
		out[i] = info.Param
	}
	return out, nil
}

// PatchChunkBatch updates multiple parameters in rawChunk in a single unwrap/patch/wrap pass.
// edits is map[paramOffset]normValue (where paramOffset is 1-based, p - 1000).
func PatchChunkBatch(rawChunk string, edits map[int]float64) (updatedChunk string, applied map[int]core.Applied, err error) {
	env, infos, err := unwrap(1, rawChunk)
	if err != nil {
		return "", nil, err
	}
	applied = make(map[int]core.Applied, len(edits))
	curInner := env.InnerPayload

	// Sort param offsets for deterministic patching order
	var offsets []int
	for off := range edits {
		offsets = append(offsets, off)
	}
	sort.Ints(offsets)

	for _, paramOffset := range offsets {
		normValue := edits[paramOffset]
		if paramOffset < 1 || paramOffset > len(infos) {
			return "", nil, fmt.Errorf("param offset %d out of range (1..%d)", paramOffset, len(infos))
		}
		info := infos[paramOffset-1]
		newText, confirmedNorm, readoutText := formatPatchedValue(normValue, info.Scale, info.OrigValue)

		// Check for discrete target override
		fxIndex := 1
		if fx, _, err := info.Param.ID.Parse(); err == nil {
			fxIndex = fx
		}
		if info.Scale == scaleDiscrete {
			if disc, ok := getAndClearDiscreteTarget(fxIndex, paramOffset); ok {
				newText = disc
				readoutText = disc
				confirmedNorm = normValue
			}
		}

		switch env.InnerFormat {
		case "xml":
			curInner, err = patchXMLPayload(curInner, info, newText)
		case "json":
			curInner, err = patchJSONPayload(curInner, info, normValue)
		case "kv":
			curInner, err = patchKVPayload(curInner, info, newText)
		default:
			return "", nil, fmt.Errorf("unknown inner format %q", env.InnerFormat)
		}
		if err != nil {
			return "", nil, fmt.Errorf("patching param %s (%d): %w", info.Param.ID, paramOffset, err)
		}

		applied[paramOffset] = core.Applied{
			Param:     info.Param.ID,
			Sent:      normValue,
			Confirmed: confirmedNorm,
			Text:      readoutText,
			Status:    core.StatusConfirmed,
		}
	}

	updatedChunk, err = wrapPayload(env, curInner)
	if err != nil {
		return "", nil, err
	}
	return updatedChunk, applied, nil
}

// PatchChunk updates parameter paramOffset (1-based index) in rawChunk and returns the updated raw chunk.
func PatchChunk(rawChunk string, paramOffset int, normValue float64) (updatedChunk string, confirmedNorm float64, readoutText string, err error) {
	updated, appMap, err := PatchChunkBatch(rawChunk, map[int]float64{paramOffset: normValue})
	if err != nil {
		return "", 0, "", err
	}
	app := appMap[paramOffset]
	return updated, app.Confirmed, app.Text, nil
}

// checkKept reads back a chunk write. Every parameter the write changed must
// read back at the value written; if one does not, the plugin kept none of the
// write, and the error says so (core.ReadOnlyError). orig is the chunk before the
// write, written the chunk that was sent, back what GetChunk read afterwards.
func checkKept(fx int, orig, written, back string) error {
	po, err := ParseChunk(fx, orig)
	if err != nil {
		return err
	}
	pw, err := ParseChunk(fx, written)
	if err != nil {
		return err
	}
	pb, err := ParseChunk(fx, back)
	if err != nil {
		return err
	}
	ov := map[core.ParamID]float64{}
	for _, p := range po {
		ov[p.ID] = p.Value
	}
	bv := map[core.ParamID]float64{}
	for _, p := range pb {
		bv[p.ID] = p.Value
	}
	for _, p := range pw {
		if math.Abs(p.Value-ov[p.ID]) <= 0.005 {
			continue // the write did not change this one
		}
		if math.Abs(bv[p.ID]-p.Value) > 0.02 {
			return core.ReadOnlyError{FX: fx, Detail: fmt.Sprintf("%s read back %.3f after writing %.3f", p.ID, bv[p.ID], p.Value)}
		}
	}
	return nil
}
