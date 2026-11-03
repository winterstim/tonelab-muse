package conformance

import (
	"context"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestFakeContractIs41Of41AndRestoresWrites(t *testing.T) {
	results := Run(context.Background(), "fake", FakeFactory)
	if len(results) != len(core.CapabilityIDs) || len(core.ContractOperations) != len(core.CapabilityIDs) {
		t.Fatalf("got %d results for %d operations", len(results), len(core.ContractOperations))
	}
	if !AllPass(results) {
		for _, result := range results {
			if result.Status != Pass {
				t.Errorf("%s: %s (%s)", result.Op, result.Status, result.Reason)
			}
		}
	}
}

func TestUnadvertisedOperationsUseSpecificFallback(t *testing.T) {
	results := Run(context.Background(), "reaper", func() (Target, error) {
		return Target{}, nil
	})
	if len(results) != len(core.ContractOperations) {
		t.Fatalf("got %d results", len(results))
	}
	for _, result := range results {
		if isLocalOperation(result.Op) {
			if result.Status != Pass {
				t.Errorf("local op %s status = %s, want pass", result.Op, result.Status)
			}
			continue
		}
		if result.Op == core.OpSaveProject {
			if result.Status != Fallback {
				t.Errorf("save status = %s", result.Status)
			}
			continue
		}
		if result.Status != Fallback {
			t.Errorf("%s status = %s, want fallback", result.Op, result.Status)
		}
		if !strings.Contains(result.Reason, "missing DAW API call:") {
			t.Errorf("%s has no specific missing-call reason: %s", result.Op, result.Reason)
		}
	}
}
