//go:build !linux && !darwin

package main

import (
	"errors"
	"runtime"
)

type tmuxManager struct {
	enabled bool
}

type tmuxAttach struct{}

func newTmuxManager() *tmuxManager {
	logf("[tmux] disabled (unsupported OS=%s)\n", runtime.GOOS)
	return &tmuxManager{enabled: false}
}

func (m *tmuxManager) listSessions(user string) ([]string, error) {
	return nil, errors.New("tmux disabled")
}

func (m *tmuxManager) createSession(user string, cols, rows uint16) (string, error) {
	return "", errors.New("tmux disabled")
}

func (m *tmuxManager) attach(client *WsClient, name string, cols, rows uint16) (*tmuxAttach, error) {
	return nil, errors.New("tmux disabled")
}

func (m *tmuxManager) detach(name string) {}

func (m *tmuxManager) detachForClient(c *WsClient) {}

func (m *tmuxManager) resize(name string, cols, rows uint16) error {
	return errors.New("tmux disabled")
}

func (m *tmuxManager) write(name string, data []byte) error {
	return errors.New("tmux disabled")
}

func (m *tmuxManager) kill(name string) error {
	return errors.New("tmux disabled")
}

func (m *tmuxManager) shutdown() {}
