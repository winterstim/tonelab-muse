package reaper

import "testing"

func TestAllowed(t *testing.T) {
	for _, a := range []string{
		"/track/1/volume", "/track/12/pan", "/track/3/mute", "/track/3/solo",
		"/track/1/fx/2/fxparam/14/value",
		"/device/track/select", "/device/fxparam/bank/select", "/device/fx/select",
		"/action/40029", "/fx/2/openui", "/track/1/fx/2/bypass", "/play", "/stop", "/action/40042",
	} {
		if !Allowed(a) {
			t.Errorf("%s should be allowed", a)
		}
	}
	for _, a := range []string{
		"", "/action/40026", "/action/x", "/action/40029/extra", // save project, junk
		"/track/1/name", "/track/1/fx/2/wetdry", "/track/1/volume/extra",
		"/track/x/volume", "/play/extra", "/record", "/device/track/../play",
		"/master/volume", "/track/1/fx/2/fxparam/14/value/str",
	} {
		if Allowed(a) {
			t.Errorf("%s should be refused", a)
		}
	}
}
