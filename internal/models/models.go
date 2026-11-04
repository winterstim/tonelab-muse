// Package models lets the user pick which model does which job. A backend
// is any endpoint that speaks Chat Completions (the Meta Model API, a local
// llama-server, vLLM/SGLang on a GPU box, a hosted partner); a role is a
// job the agent has. Each backend is probed for what it can do, so a role
// never lands on a model that cannot do it.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Caps is what a backend proved it can do when probed.
type Caps struct {
	Tools   bool      `json:"tools"`  // function calling (planner)
	Vision  bool      `json:"vision"` // images in (spectrograms, plugin windows)
	Speech  bool      `json:"speech"` // speech to text (voice)
	Checked time.Time `json:"checked"`
	Note    string    `json:"note,omitempty"`
}

// Backend is one model at one endpoint.
type Backend struct {
	Name    string `json:"name"`              // shown to the user, unique
	Kind    string `json:"kind"`              // "meta" (Meta Model API) or "compatible" (any compatible server)
	BaseURL string `json:"base_url"`          // e.g. http://127.0.0.1:8080/v1
	Model   string `json:"model"`             // model id at that endpoint
	KeyEnv  string `json:"key_env,omitempty"` // env var with the key; "" = none
	Local   bool   `json:"local"`             // runs on this Mac: nothing leaves it
	// Serve, for a local backend the app starts itself: the GGUF file.
	Serve string `json:"serve,omitempty"`
	Caps  Caps   `json:"caps"`
}

// Roles names the backend for each job.
type Roles struct {
	Planner string `json:"planner"` // reads the request, plans, uses tools
	Eyes    string `json:"eyes"`    // looks at spectrograms and plugin windows
	Voice   string `json:"voice"`   // speech to text
}

// Registry is the user's models and who does what.
type Registry struct {
	Backends []Backend `json:"backends"`
	Roles    Roles     `json:"roles"`
	Mode     string    `json:"mode"` // cloud, private, custom
}

const metaURL = "https://api.meta.ai/v1"

// Defaults: Meta's hosted models, and Muse Glimmer on this Mac.
func Defaults(glimmerFile string) Registry {
	r := Registry{
		Backends: []Backend{
			{Name: "Muse Spark 1.3", Kind: "meta", BaseURL: metaURL, Model: "muse-spark-1.3", KeyEnv: "MODEL_API_KEY",
				Caps: Caps{Tools: true, Vision: true}},
			{Name: "Muse Wonder 1.0", Kind: "meta", BaseURL: metaURL, Model: "muse-wonder-1.0", KeyEnv: "MODEL_API_KEY",
				Caps: Caps{Tools: true, Vision: true}},
			{Name: "Muse Voice Transcribe", Kind: "meta", BaseURL: metaURL, Model: "muse-voice-transcribe-1.0", KeyEnv: "MODEL_API_KEY",
				Caps: Caps{Speech: true}},
			{Name: "Muse Glimmer (this Mac)", Kind: "compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "muse-glimmer-30b",
				Local: true, Serve: glimmerFile},
			{Name: "Muse Spark Open", Kind: "compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "meta-models/muse-spark-open-1026",
				Local: true, Caps: Caps{Tools: true, Vision: true}},
		},
	}
	r.SetMode("cloud")
	return r
}

// SetMode assigns roles for a preset. Private keeps every role on this
// Mac; a role no local model can do is left empty (that feature turns off)
// rather than quietly sent to the cloud.
func (r *Registry) SetMode(mode string) {
	r.Mode = mode
	switch mode {
	case "cloud":
		r.Roles = Roles{Planner: "Muse Spark 1.3", Eyes: "Muse Spark 1.3", Voice: "Muse Voice Transcribe"}
	case "private":
		r.Roles = Roles{}
		for _, b := range r.Backends {
			if !b.Local {
				continue
			}
			if b.Caps.Tools && r.Roles.Planner == "" {
				r.Roles.Planner = b.Name
			}
			if b.Caps.Vision && r.Roles.Eyes == "" {
				r.Roles.Eyes = b.Name
			}
			if b.Caps.Speech && r.Roles.Voice == "" {
				r.Roles.Voice = b.Name
			}
		}
	}
}

// Get finds a backend by name.
func (r Registry) Get(name string) (Backend, bool) {
	for _, b := range r.Backends {
		if b.Name == name {
			return b, true
		}
	}
	return Backend{}, false
}

// Put adds or replaces a backend.
func (r *Registry) Put(b Backend) error {
	if b.Name == "" || b.BaseURL == "" || b.Model == "" {
		return errors.New("a model needs a name, an address and a model id")
	}
	if b.Kind == "" {
		b.Kind = "compatible"
	}
	for i := range r.Backends {
		if r.Backends[i].Name == b.Name {
			r.Backends[i] = b
			return nil
		}
	}
	r.Backends = append(r.Backends, b)
	return nil
}

// Check says what a role assignment cannot do, if anything.
func (r Registry) Check() []string {
	var out []string
	need := []struct {
		role, name string
		ok         func(Caps) bool
		what       string
	}{
		{"planner", r.Roles.Planner, func(c Caps) bool { return c.Tools }, "tools"},
		{"eyes", r.Roles.Eyes, func(c Caps) bool { return c.Vision }, "images"},
		{"voice", r.Roles.Voice, func(c Caps) bool { return c.Speech }, "speech"},
	}
	for _, n := range need {
		if n.name == "" {
			continue
		}
		b, ok := r.Get(n.name)
		if !ok {
			out = append(out, fmt.Sprintf("%s: no model named %q", n.role, n.name))
		} else if !n.ok(b.Caps) {
			out = append(out, fmt.Sprintf("%s: %s has not shown it can do %s", n.role, n.name, n.what))
		}
	}
	return out
}

// Load reads the registry, or returns the defaults when there is none.
func Load(path, glimmerFile string) (Registry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(glimmerFile), nil
	}
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	return r, json.Unmarshal(b, &r)
}

func (r Registry) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
