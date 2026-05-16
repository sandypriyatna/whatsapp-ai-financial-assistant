package whatsapp

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Messenger abstracts WhatsApp message sending for testability.
type Messenger interface {
	SendText(ctx context.Context, recipient string, text string) error
	SendPresence(ctx context.Context, recipient string) error
	// JID-based variants for group support.
	SendTextToJID(ctx context.Context, jid types.JID, text string) error
	SendPresenceToJID(ctx context.Context, jid types.JID) error
}

type WhatsAppClient struct {
	client *whatsmeow.Client
	log    waLog.Logger
}

func NewWhatsAppClient(client *whatsmeow.Client) *WhatsAppClient {
	return &WhatsAppClient{
		client: client,
	}
}

func (w *WhatsAppClient) SendText(ctx context.Context, recipient string, text string) error {
	// If recipient already has a domain (e.g. "xxx@g.us" for groups), use as-is.
	// Otherwise append the default personal chat suffix.
	raw := recipient
	if !strings.Contains(raw, "@") {
		raw = raw + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return fmt.Errorf("invalid JID %s: %w", recipient, err)
	}
	return w.SendTextToJID(ctx, jid, text)
}

func (w *WhatsAppClient) SendTextToJID(ctx context.Context, jid types.JID, text string) error {
	_, err := w.client.SendMessage(ctx, jid, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

func (w *WhatsAppClient) SendPresence(ctx context.Context, recipient string) error {
	raw := recipient
	if !strings.Contains(raw, "@") {
		raw = raw + "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return fmt.Errorf("invalid JID %s: %w", recipient, err)
	}
	return w.SendPresenceToJID(ctx, jid)
}

func (w *WhatsAppClient) SendPresenceToJID(ctx context.Context, jid types.JID) error {
	w.client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText)

	delay := time.Duration(500+rand.Intn(1001)) * time.Millisecond
	time.Sleep(delay)

	w.client.SendChatPresence(ctx, jid, types.ChatPresencePaused, types.ChatPresenceMediaText)

	return nil
}
