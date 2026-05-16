package whatsapp

import (
	"context"
	"math/rand"
	"strings"
	"time"
)

const (
	minTypingDelayMs = 500
	maxTypingDelayMs = 1500
)

// RandomTypingDelay returns an anti-ban friendly randomized typing delay.
func RandomTypingDelay() time.Duration {
	ms := minTypingDelayMs + rand.Intn(maxTypingDelayMs-minTypingDelayMs+1)
	return time.Duration(ms) * time.Millisecond
}

// SendPresenceBeforeReply emits typing presence before reply.
func SendPresenceBeforeReply(ctx context.Context, messenger Messenger, recipient string) {
	if messenger == nil {
		return
	}
	if strings.TrimSpace(recipient) == "" {
		return
	}

	// Best-effort presence; ignore error so chat flow still proceeds.
	_ = messenger.SendPresence(ctx, recipient)
}

// SendTextWithPresence sends typing presence first, then the text reply.
func SendTextWithPresence(ctx context.Context, messenger Messenger, recipient, text string) error {
	SendPresenceBeforeReply(ctx, messenger, recipient)
	return messenger.SendText(ctx, recipient, text)
}
