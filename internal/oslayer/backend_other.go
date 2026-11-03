//go:build !darwin

package oslayer

import "errors"

type otherBackend struct{}

func defaultBackend() Backend {
	return &otherBackend{}
}

func (b *otherBackend) CheckAccessibility() error {
	return ErrAccessibilityPermission
}

func (b *otherBackend) ListWindows() ([]WindowInfo, error) {
	return nil, errors.New("listing windows is only supported on macOS")
}

func (b *otherBackend) SendCmdS(pid int) error {
	return errors.New("sending Cmd+S is only supported on macOS")
}

func (b *otherBackend) GetWindowTitle(pid int, windowID int) (string, error) {
	return "", errors.New("querying window title is only supported on macOS")
}

func (b *otherBackend) Activate(pid int) error {
	return errors.New("activating window is only supported on macOS")
}
