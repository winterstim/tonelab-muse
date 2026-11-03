package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitForBridgeReadyRetriesUntilPingSucceeds(t *testing.T) {
	calls := 0
	err := waitForBridgeReady(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("not ready")
		}
		return "7.0", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("ping attempts = %d, want 3", calls)
	}
}

func TestWaitForBridgeReadyStopsAtThreeSeconds(t *testing.T) {
	start := time.Now()
	err := waitForBridgeReady(context.Background(), func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err == nil {
		t.Fatal("expected readiness timeout")
	}
	if elapsed := time.Since(start); elapsed > 3500*time.Millisecond {
		t.Fatalf("readiness retry exceeded 3 seconds: %s", elapsed)
	}
}

func TestConformanceCLIWritesMarkdownAndJSON(t *testing.T) {
	jsonPath := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	code := runConformanceCommand([]string{"--daw", "fake", "--json", jsonPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "| op | status | reason |\n") {
		t.Fatalf("Markdown table header missing: %q", stdout.String()[:min(len(stdout.String()), 80)])
	}
	var rows []map[string]any
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 41 {
		t.Fatalf("JSON contains %d rows, want 41", len(rows))
	}
	for _, row := range rows {
		if row["status"] != "pass" || row["op"] == "" || row["reason"] == "" {
			t.Errorf("invalid conformance row: %#v", row)
		}
	}
}
