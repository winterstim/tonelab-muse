//go:build !darwin && !windows

package eyes

import (
	"context"
	"errors"
)

type fallbackOS struct{}

func (fallbackOS) Windows() ([]Window, error) {
	return nil, errors.New("eyes: window listing not supported on this platform")
}

func (fallbackOS) Capture(context.Context, int, string) error {
	return errors.New("eyes: window capture not supported on this platform")
}

func (fallbackOS) Record(context.Context, string, float64, string) error {
	return errors.New("eyes: audio capture not supported on this platform")
}

func defaultOS() OS {
	return fallbackOS{}
}
