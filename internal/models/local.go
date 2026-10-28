package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Serve starts llama-server for a local backend when it is not already
// answering, sized to this Mac by llama.cpp's own --fit, and waits until
// the model is loaded. The user never configures it.
func Serve(ctx context.Context, b Backend, logDir string) error {
	if !b.Local || b.Serve == "" {
		return nil
	}
	if healthy(b) {
		return nil
	}
	if _, err := os.Stat(b.Serve); err != nil {
		return fmt.Errorf("%s: model file missing (%s)", b.Name, b.Serve)
	}
	bin, err := exec.LookPath("llama-server")
	if err != nil {
		return errors.New("llama.cpp is not installed (brew install llama.cpp)")
	}
	u, err := url.Parse(b.BaseURL)
	if err != nil {
		return err
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" {
		return fmt.Errorf("%s: a local model must listen on this Mac only", b.Name)
	}
	os.MkdirAll(logDir, 0o755)
	log, err := os.Create(filepath.Join(logDir, "llama-server.log"))
	if err != nil {
		return err
	}
	args := []string{"-m", b.Serve, "--host", "127.0.0.1", "--port", u.Port(), "--alias", b.Model,
		// --fit sizes the GPU share to free memory; the margin leaves room
		// for the compute buffers, which on a 16 GB Mac would otherwise
		// run out on the first request
		"--jinja", "--fit", "on", "--fit-target", "3072", "-c", "16384", "-ub", "512"}
	if mm := mmproj(b.Serve); mm != "" {
		args = append(args, "--mmproj", mm)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	deadline := time.Now().Add(5 * time.Minute) // loading 15 GB takes a while
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if healthy(b) {
			return nil
		}
		if cmd.ProcessState != nil {
			return fmt.Errorf("%s stopped while loading; see %s", b.Name, log.Name())
		}
	}
	return fmt.Errorf("%s did not finish loading in 5 minutes; see %s", b.Name, log.Name())
}

// mmproj finds a vision projector next to the model, if one was downloaded.
func mmproj(model string) string {
	ms, _ := filepath.Glob(filepath.Join(filepath.Dir(model), "mmproj-*.gguf"))
	if len(ms) > 0 {
		return ms[0]
	}
	return ""
}

func healthy(b Backend) bool {
	root := strings.TrimSuffix(strings.TrimRight(b.BaseURL, "/"), "/v1")
	c := http.Client{Timeout: time.Second}
	r, err := c.Get(root + "/health")
	if err != nil {
		return false
	}
	r.Body.Close()
	return r.StatusCode == http.StatusOK
}
