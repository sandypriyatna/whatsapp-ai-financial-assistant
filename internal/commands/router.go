package commands

import (
	"context"
	"strings"
)

type HandlerFunc func(ctx context.Context, args string) string

type Router struct {
	commands map[string]HandlerFunc
}

func NewRouter() *Router {
	return &Router{
		commands: make(map[string]HandlerFunc),
	}
}

// Register adds a command handler. Command should include "/" prefix.
func (r *Router) Register(command string, handler HandlerFunc) {
	if r == nil || handler == nil {
		return
	}
	cmd := normalizeCommand(command)
	if cmd == "" {
		return
	}
	r.commands[cmd] = handler
}

// Route checks whether text starts with a registered command (case-insensitive).
// Returns handler result and true if matched; otherwise ("", false).
func (r *Router) Route(ctx context.Context, text string) (string, bool) {
	if r == nil {
		return "", false
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}

	parts := strings.SplitN(trimmed, " ", 2)
	cmd := normalizeCommand(parts[0])

	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	handler, ok := r.commands[cmd]
	if !ok {
		return "", false
	}

	return handler(ctx, args), true
}

func normalizeCommand(command string) string {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if cmd == "" {
		return ""
	}
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	return cmd
}
