package eyes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OS is everything eyes needs from macOS that needs a permission: the
// window list, window captures and app audio. macOS ties that
// permission to the exact binary, so the agent does not do this itself
// (every rebuild would lose them): a small helper app that rarely changes
// owns the permission and does it on request (see cmd/hands).
type OS interface {
	Windows() ([]Window, error)
	Capture(ctx context.Context, id int, path string) error
	// Record captures seconds of one app's own audio (by bundle id) to a WAV.
	Record(ctx context.Context, bundle string, seconds float64, path string) error
}

// msg is one request or reply between the agent and the helper.
type msg struct {
	Op   string   `json:"op"`
	X    float64  `json:"x,omitempty"`
	Y    float64  `json:"y,omitempty"`
	ID   int      `json:"id,omitempty"`
	Path string   `json:"path,omitempty"`
	Text string   `json:"text,omitempty"`
	Err  string   `json:"err,omitempty"`
	Wins []Window `json:"windows,omitempty"`
}

// Socket is where the helper listens.
func Socket() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tonelab-muse", "hands.sock")
}

// Remote talks to the helper app, starting it when it is not running.
type Remote struct {
	Sock string // "" = Socket()
	App  string // helper app bundle to open; "" = ~/Applications/Muse Hands.app
}

func (r Remote) call(m msg) (msg, error) {
	sock := r.Sock
	if sock == "" {
		sock = Socket()
	}
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		// nothing answers: a socket file left by a helper that quit would
		// stop the start below from waiting for a new one
		os.Remove(sock)
		if err := r.start(sock); err != nil {
			return msg{}, err
		}
		if c, err = net.DialTimeout("unix", sock, time.Second); err != nil {
			return msg{}, err
		}
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30*time.Second + time.Duration(m.X*float64(time.Second))))
	if err := json.NewEncoder(c).Encode(m); err != nil {
		return msg{}, err
	}
	var out msg
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&out); err != nil {
		return msg{}, fmt.Errorf("hands: %w", err)
	}
	if out.Err != "" {
		return out, errors.New(out.Err)
	}
	return out, nil
}

func (r Remote) start(sock string) error {
	app := r.App
	if app == "" {
		home, _ := os.UserHomeDir()
		app = filepath.Join(home, "Applications", "Muse Hands.app")
	}
	if _, err := os.Stat(app); err != nil {
		return fmt.Errorf("the helper app is not installed (%s): run scripts/install-hands.sh once", app)
	}
	if out, err := exec.Command("open", "-g", app).CombinedOutput(); err != nil {
		return fmt.Errorf("open helper: %v %s", err, out)
	}
	for i := 0; i < 40; i++ {
		if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the helper app did not start")
}

func (r Remote) Windows() ([]Window, error) {
	m, err := r.call(msg{Op: "windows"})
	return m.Wins, err
}
func (r Remote) Capture(ctx context.Context, id int, path string) error {
	_, err := r.call(msg{Op: "capture", ID: id, Path: path})
	return err
}

// Serve runs the helper side: one request per connection, done with this
// process's own permissions.
func Serve(ctx context.Context, sock string, o OS) error {
	os.MkdirAll(filepath.Dir(sock), 0o700)
	os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600) // this user only
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			var in msg
			if err := json.NewDecoder(c).Decode(&in); err != nil {
				return
			}
			out := handle(ctx, o, in)
			json.NewEncoder(c).Encode(out)
		}(c)
	}
}

func handle(ctx context.Context, o OS, in msg) msg {
	var out msg
	var err error
	switch in.Op {
	case "windows":
		out.Wins, err = o.Windows()
	case "capture":
		err = o.Capture(ctx, in.ID, in.Path)
	case "record":
		if in.X <= 0 || in.X > 60 {
			err = errors.New("record 0-60 seconds")
		} else {
			err = o.Record(ctx, in.Text, in.X, in.Path)
		}
	default:
		err = fmt.Errorf("unknown op %q", in.Op)
	}
	if err != nil {
		out.Err = err.Error()
	}
	return out
}

// screencapture writes one window to path.
func screencapture(ctx context.Context, id int, path string) error {
	if out, err := exec.CommandContext(ctx, "screencapture", "-x", "-o", "-l", fmt.Sprint(id), path).CombinedOutput(); err != nil {
		return fmt.Errorf("screencapture: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// quoted is the app name lsappinfo prints first, in quotes.
func quoted(out string) string {
	s := strings.TrimSpace(out)
	if i := strings.Index(s[1:], `"`); strings.HasPrefix(s, `"`) && i >= 0 {
		return s[1 : i+1]
	}
	return s
}

// AppName is the process name macOS lists a running app's windows under,
// from its bundle id (e.g. com.ableton.live → "Live"); "" if not running.
func AppName(bundle string) string {
	if bundle == "" {
		return ""
	}
	out, err := exec.Command("lsappinfo", "info", "-only", "name", "-app", bundle).Output()
	if err != nil {
		return ""
	}
	return quoted(string(out))
}

func (r Remote) Record(ctx context.Context, bundle string, seconds float64, path string) error {
	_, err := r.call(msg{Op: "record", Text: bundle, X: seconds, Path: path})
	return err
}
