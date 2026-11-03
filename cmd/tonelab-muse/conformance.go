package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/winterstim/tonelab-muse/internal/conformance"
	"github.com/winterstim/tonelab-muse/internal/daw/oscmap"
	"github.com/winterstim/tonelab-muse/internal/daw/oscmap/bundled"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

func runConformanceCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	daw := fs.String("daw", "", "DAW to check: fake, reaper, or ableton")
	jsonPath := fs.String("json", "conformance.json", "write the JSON report to this path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *daw != "fake" && *daw != "reaper" && *daw != "ableton" {
		fmt.Fprintln(stderr, "conformance: --daw must be fake, reaper, or ableton")
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "conformance: unexpected positional arguments")
		return 2
	}

	var open conformance.Factory
	switch *daw {
	case "fake":
		open = conformance.FakeFactory
	case "reaper":
		reloadedBridge := false
		open = func() (conformance.Target, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			conn, err := reaper.Dial("127.0.0.1:9000", "127.0.0.1:8000")
			if err != nil {
				return conformance.Target{}, err
			}
			r := reaper.New(conn)
			if !reloadedBridge {
				bridge := reaper.NewContractExecutor(r).Bridge
				if _, err := bridge.Install(); err != nil {
					r.Close()
					return conformance.Target{}, fmt.Errorf("install REAPER bridge: %w", err)
				}
				if err := bridge.Reload(ctx); err != nil {
					r.Close()
					return conformance.Target{}, fmt.Errorf("reload REAPER bridge: %w", err)
				}
				reloadedBridge = true
			}
			bridge := reaper.NewContractExecutor(r).Bridge
			if err := waitForBridgeReady(ctx, bridge.Ping); err != nil {
				r.Close()
				return conformance.Target{}, fmt.Errorf("REAPER bridge readiness ping: %w", err)
			}
			if err := r.Alive(ctx); err != nil {
				r.Close()
				return conformance.Target{}, fmt.Errorf("REAPER live bridge check: %w", err)
			}
			return conformance.Target{Executor: reaper.NewContractExecutor(r), Close: r.Close}, nil
		}
	case "ableton":
		var m oscmap.Map
		raw, readErr := bundled.FS.ReadFile("ableton.json")
		if readErr != nil {
			fmt.Fprintf(stderr, "conformance: read bundled Ableton map: %v\n", readErr)
			return 1
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			fmt.Fprintf(stderr, "conformance: parse bundled Ableton map: %v\n", err)
			return 1
		}
		if err := m.Check(); err != nil {
			fmt.Fprintf(stderr, "conformance: invalid Ableton map: %v\n", err)
			return 1
		}
		open = func() (conformance.Target, error) {
			d, err := oscmap.Open(m)
			if err != nil {
				return conformance.Target{}, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := d.Alive(ctx); err != nil {
				d.Close()
				return conformance.Target{}, fmt.Errorf("ableton live bridge check: %w", err)
			}
			executor, _ := any(d).(core.ContractExecutor)
			return conformance.Target{Executor: executor, Close: d.Close}, nil
		}
	}

	results := conformance.Run(context.Background(), *daw, open)
	fmt.Fprint(stdout, conformance.Markdown(results))
	if err := conformance.WriteJSON(*jsonPath, results); err != nil {
		fmt.Fprintf(stderr, "conformance: write JSON: %v\n", err)
		return 1
	}
	counts := map[conformance.Status]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	fmt.Fprintf(stderr, "%s: %d pass, %d fail, %d fallback, %d unsupported; JSON: %s\n", *daw,
		counts[conformance.Pass], counts[conformance.Fail], counts[conformance.Fallback], counts[conformance.Unsupported], *jsonPath)
	if counts[conformance.Fail] > 0 || (*daw == "fake" && !conformance.AllPass(results)) {
		return 1
	}
	return 0
}

func waitForBridgeReady(ctx context.Context, ping func(context.Context) (string, error)) error {
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return fmt.Errorf("not ready within 3 seconds: %w", lastErr)
		}
		attemptTimeout := 300 * time.Millisecond
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		_, err := ping(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		pause := 100 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
