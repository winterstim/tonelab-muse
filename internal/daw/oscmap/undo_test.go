package oscmap

import (
	"context"
	"testing"
)

func TestOptionalUndoAndSnapshotMapCommands(t *testing.T) {
	m := Map{Name: "fixture", Port: 9000, ReplyPort: 9001,
		Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"), DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/params", "i:{track}", "i:{fx}"), ParamValues: cmd("/values", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"), SetParam: Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		BeginUndoAction:        &Command{Send: []string{"/undo/begin", "s:{name}"}, Reply: "/undo/begin", Success: "started"},
		EndUndoAction:          &Command{Send: []string{"/undo/end", "s:{name}", "i:{value}"}, Reply: "/undo/end", Success: "committed", CancelSuccess: "cancelled"},
		UndoActionCurrent:      &Command{Send: []string{"/undo/current", "s:{name}"}, Reply: "/undo/current"},
		CaptureSessionSnapshot: &Command{Send: []string{"/session/capture"}, Reply: "/session/capture"},
		RestoreSessionSnapshot: &Command{Send: []string{"/session/restore", "s:{name}"}, Reply: "/session/restore", Success: "restored"},
	}
	if err := m.Check(); err != nil {
		t.Fatalf("optional commands rejected: %v", err)
	}
	d := &DAW{M: m}
	if !d.SupportsUndoActionGrouping() || !d.SupportsRequestSnapshot() {
		t.Fatal("optional command pairs were not recognized")
	}
	addr, args, err := m.BeginUndoAction.build(vars{name: "Agent action"})
	if err != nil || addr != "/undo/begin" || args[0] != "Agent action" {
		t.Fatalf("begin action build: %s %#v %v", addr, args, err)
	}
	addr, args, err = m.EndUndoAction.build(vars{name: "Agent action", value: 1})
	if err != nil || addr != "/undo/end" || args[0] != "Agent action" || args[1] != int32(1) {
		t.Fatalf("end action build: %s %#v %v", addr, args, err)
	}
}

func TestMapWithoutSessionSnapshotCommandsDoesNotClaimSupport(t *testing.T) {
	d := &DAW{M: Map{}}
	if d.SupportsRequestSnapshot() {
		t.Fatal("empty map claimed exact session snapshots")
	}
	if _, err := d.CaptureRequestSnapshot(context.Background()); err == nil {
		t.Fatal("missing snapshot commands returned fake state")
	}
	if err := d.RestoreRequestSnapshot(context.Background(), []byte("state")); err == nil {
		t.Fatal("missing restore command returned success")
	}
}
