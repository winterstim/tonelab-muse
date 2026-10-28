//go:build darwin

// Command hands is the small helper app (Muse Hands) that owns the macOS
// permission for seeing windows and recording app audio. macOS
// ties that permission to the exact binary, so this stays a separate,
// rarely rebuilt program; the agent sends it requests over a socket that
// only this user can open. On first start macOS asks for it.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/winterstim/tonelab-muse/internal/eyes"
)

func main() {
	go eyes.AskAccess() // serve meanwhile, so the agent can report what is missing
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := eyes.Serve(ctx, eyes.Socket(), eyes.Local{}); err != nil {
		fmt.Fprintln(os.Stderr, "hands:", err)
		os.Exit(1)
	}
}
