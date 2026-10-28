package eyes

import "os/exec"

// Window is one on-screen window.
type Window struct {
	ID, Pid     int
	Owner, Name string
	X, Y, W, H  int // bounds in screen points
}

// Contains reports whether a screen point falls inside the window.
func (w Window) Contains(x, y float64) bool {
	return x >= float64(w.X) && y >= float64(w.Y) && x < float64(w.X+w.W) && y < float64(w.Y+w.H)
}

func OpenSettings(page string) {
	exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?"+page).Run()
}

func frontOf(ws []Window, app string) (Window, bool) {
	if app == "" {
		return Window{}, false
	}
	for _, w := range ws {
		if w.Owner == app && w.W > 150 && w.H > 100 {
			return w, true
		}
	}
	return Window{}, false
}
