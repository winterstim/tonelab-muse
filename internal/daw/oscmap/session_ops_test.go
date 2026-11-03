package oscmap

import "testing"

func TestOptionalSessionMapCommandsAndTemplates(t *testing.T) {
	m := Map{
		Name: "fixture", Port: 9000, ReplyPort: 9001, Base: 1,
		Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/params", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/values", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:         Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		SetTimeSignature: &Command{Send: []string{"/meter", "i:{numerator}", "i:{denominator}"}, Reply: "/meter", Success: "ok"},
		SetTrackArm:      &Command{Send: []string{"/track/arm", "i:{track}", "i:{value}"}, Reply: "/track/arm"},
		SetTrackMonitor:  &Command{Send: []string{"/track/monitor", "i:{track}", "i:{value}"}, Reply: "/track/monitor"},
		SetTrackInput:    &Command{Send: []string{"/track/input", "i:{track}", "i:{value}"}, Reply: "/track/input"},
		RemoveSend:       &Command{Send: []string{"/send/remove", "i:{track}", "i:{send}"}, Reply: "/send/remove", Success: "ok"},
		ListMarkers:      &Command{Send: []string{"/markers"}, Reply: "/markers"},
		CreateMarker:     &Command{Send: []string{"/marker/create", "f:{start}", "f:{end}", "i:{flag}", "s:{name}"}, Reply: "/marker/create"},
		GoToMarker:       &Command{Send: []string{"/marker/goto", "i:{marker}"}, Reply: "/marker/goto", Success: "ok"},
		SaveProject:      &Command{Send: []string{"/project/save"}, Reply: "/project/save", Success: "ok"},
	}
	if err := m.Check(); err != nil {
		t.Fatalf("optional session commands rejected: %v", err)
	}

	addr, args, err := m.CreateMarker.build(vars{start: 1.25, end: 3.5, flag: 1, name: "A region"})
	if err != nil {
		t.Fatal(err)
	}
	if addr != "/marker/create" || len(args) != 4 || args[0] != float32(1.25) || args[1] != float32(3.5) || args[2] != int32(1) || args[3] != "A region" {
		t.Fatalf("unexpected marker command build: %s %#v", addr, args)
	}
	addr, args, err = m.RemoveSend.build(vars{track: 2, send: 4})
	if err != nil {
		t.Fatal(err)
	}
	if addr != "/send/remove" || args[0] != int32(2) || args[1] != int32(4) {
		t.Fatalf("unexpected remove-send build: %s %#v", addr, args)
	}
}

func TestMutationMapRequiresExactPositiveStatus(t *testing.T) {
	for _, role := range []string{"meter", "remove send", "go to marker", "save project"} {
		t.Run(role, func(t *testing.T) {
			m := Map{
				Name: "fixture", Port: 9000, ReplyPort: 9001,
				Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"),
				DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/params", "i:{track}", "i:{fx}"),
				ParamValues: cmd("/values", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
				SetParam: Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
			}
			c := &Command{Send: []string{"/mutate"}, Reply: "/mutate"}
			switch role {
			case "meter":
				m.SetTimeSignature = c
			case "remove send":
				m.RemoveSend = c
			case "go to marker":
				m.GoToMarker = c
			case "save project":
				m.SaveProject = c
			}
			if err := m.Check(); err == nil {
				t.Fatal("mutation without exact success token passed map check")
			}
		})
	}
}

func TestOptionalSessionMapCommandStillRejectsUnsafeAddress(t *testing.T) {
	m := Map{
		Name: "fixture", Port: 9000, ReplyPort: 9001,
		Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/params", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/values", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:    Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		SaveProject: &Command{Send: []string{"/project/quit"}},
	}
	if err := m.Check(); err == nil {
		t.Fatal("unsafe optional project command was accepted")
	}
}
