package muse

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// responses posts a request body to the Responses API and returns the raw
// answer.
func (c *Client) responses(ctx context.Context, b []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/responses", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	key := c.key()
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	raw = []byte(redactSecret(string(raw), key))
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return nil, statusError{resp.StatusCode, msg}
	}
	return raw, nil
}

func dataURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := "image/png"
	if e := strings.ToLower(filepath.Ext(path)); e == ".jpg" || e == ".jpeg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}
