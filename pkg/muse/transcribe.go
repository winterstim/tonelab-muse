package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// toneWords bias recognition toward the language of tone, which a speech
// model rarely hears; Words adds what is in the session right now.
var toneWords = []string{"EQ", "presence", "treble", "bass", "mids", "gain", "drive", "crunch", "mud", "fizz", "low end", "high end", "amp", "cab", "noise gate"}

// Voice implements core.Transcriber with Muse Voice Transcribe in
// push-to-talk mode. The audio must be mono 16-bit PCM WAV at 16 or 24 kHz.
type Voice struct {
	*Client
	Model string          // the registry's voice role
	Words func() []string // plugin and control names in the session; optional
}

func (v Voice) keywords() []string {
	out := append([]string(nil), toneWords...)
	if v.Words != nil {
		out = append(out, v.Words()...)
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func (v Voice) Transcribe(ctx context.Context, wav []byte) (string, error) {
	// a WAV at another rate or layout is converted; anything else goes as it
	// is and the service says what it cannot take
	if conv, err := pushToTalkWAV(wav); err == nil {
		wav = conv
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	settings, _ := json.Marshal(map[string]any{
		"model":         v.Model,
		"audioEncoding": "WAV",
		"mode":          "PUSH_TO_TALK",
		"keywords":      v.keywords(),
	})
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="request"`)
	h.Set("Content-Type", "application/json")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	pw.Write(settings)
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="audio"; filename="speech.wav"`)
	h.Set("Content-Type", "audio/wav")
	if pw, err = mw.CreatePart(h); err != nil {
		return "", err
	}
	pw.Write(wav)
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(v.BaseURL, "/")+"/asr/transcribe", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.Key)
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", statusError{resp.StatusCode, msg}
	}
	var r struct {
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("muse: bad transcript JSON: %w", err)
	}
	return strings.TrimSpace(r.Transcript), nil
}
