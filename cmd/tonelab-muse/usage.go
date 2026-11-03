package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/muse"
)

// usageLog keeps the per-call token split from the API. With MUSE_USAGE_LOG
// set to a path, each call is appended there as one JSON line; the run's
// totals are printed to stderr when the request ends.
type usageLog struct {
	mu                        sync.Mutex
	file                      *os.File
	calls, in, out, reasoning int
}

func newUsageLog(llm *muse.Client) *usageLog {
	u := &usageLog{}
	if path := os.Getenv("MUSE_USAGE_LOG"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			u.file = f
		}
	}
	llm.OnUsage = u.record
	return u
}

func (u *usageLog) record(model string, c core.Usage) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.in += c.In
	u.out += c.Out
	u.reasoning += c.Reasoning
	if u.file != nil {
		line, _ := json.Marshal(map[string]any{"call": u.calls, "model": model, "in": c.In, "out": c.Out,
			"cached": c.Cached, "reasoning": c.Reasoning})
		_, _ = u.file.Write(append(line, '\n'))
	}
}

func (u *usageLog) summary() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return fmt.Sprintf("usage: %d calls, in %d, out %d (reasoning %d), total %d",
		u.calls, u.in, u.out, u.reasoning, u.in+u.out)
}

func (u *usageLog) close() {
	if u.file != nil {
		_ = u.file.Close()
	}
}
