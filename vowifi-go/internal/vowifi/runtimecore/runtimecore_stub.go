//go:build !linux

package runtimecore

import (
	"context"
	"errors"
)

func StartAndWaitEPDG(context.Context, StartInput) (Session, error) {
	return nil, ErrUnsupportedPlatform
}

type stubSession struct{}

func (stubSession) Snapshot() TunnelSnapshot { return TunnelSnapshot{} }
func (stubSession) Shutdown()                {}
func (stubSession) WaitDoneContext(context.Context) error {
	return nil
}
func (stubSession) TriggerMOBIKE(string, string) error {
	return errors.New("mobike unsupported on this platform")
}
