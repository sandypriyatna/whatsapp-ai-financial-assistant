package whatsapp

import (
	"context"
	"log"
	"strings"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Handler struct {
	messenger     Messenger
	ownerNumbers  map[string]bool // individual JID user part → true
	allowedGroups map[string]bool // group JID user part → true
	onMessage     func(ctx context.Context, sender string, text string) string
}

func NewHandler(
	m Messenger,
	ownerIDs []string,
	onMessage func(ctx context.Context, sender string, text string) string,
	allowedGroups ...string,
) *Handler {
	if onMessage == nil {
		onMessage = func(context.Context, string, string) string { return "" }
	}

	groups := make(map[string]bool, len(allowedGroups))
	for _, g := range allowedGroups {
		g = strings.TrimSpace(g)
		if g != "" {
			groups[g] = true
		}
	}
	owners := make(map[string]bool, len(ownerIDs))
	for _, o := range ownerIDs {
		o = strings.TrimSpace(o)
		if o != "" {
			owners[o] = true
		}
	}

	return &Handler{
		messenger:     m,
		ownerNumbers:  owners,
		allowedGroups: groups,
		onMessage:     onMessage,
	}
}

func (h *Handler) Register(client *whatsmeow.Client) {
	if client == nil {
		return
	}

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			go h.handleMessage(context.Background(), v)
		}
	})
}

func (h *Handler) handleMessage(ctx context.Context, evt *events.Message) {
	if evt == nil {
		return
	}

	// Log all incoming messages with JID info for discoverability.
	if evt.Info.IsGroup {
		log.Printf("📩 [GROUP] JID: %s | Sender: %s | Group: %s",
			evt.Info.Chat.User, evt.Info.Sender.User, evt.Info.Chat.String())
	} else {
		log.Printf("📩 [DM] Sender: %s | Text: %s", evt.Info.Sender.User, getTextFromMessage(evt.Message))
	}

	// Determine reply target and authorization.
	var replyTo string
	if evt.Info.IsGroup {
		// Group message: check if this group is allowed.
		if !h.allowedGroups[evt.Info.Chat.User] {
			return // not an allowed group
		}
		// Reply goes to the group, not the individual sender.
		replyTo = evt.Info.Chat.String()
	} else {
		// Personal chat: owner whitelist check.
		if !h.ownerNumbers[evt.Info.Sender.User] {
			log.Printf("🚫 [REJECTED] Unauthorized DM from: %s", evt.Info.Sender.User)
			return
		}
		// Use full JID string (could be @s.whatsapp.net or @lid).
		replyTo = evt.Info.Sender.String()
	}

	// Extract text from all supported variants.
	text := strings.TrimSpace(getTextFromMessage(evt.Message))
	if text == "" {
		return
	}

	// Run callback with the REPLY TARGET as sender so reminders
	// and other services correctly address the group/DM.
	log.Printf("🤖 [AI] Processing message with router for sender: %s...", replyTo)
	response := strings.TrimSpace(h.onMessage(ctx, replyTo, text))
	if response == "" {
		log.Printf("⚠️ [AI] Router returned an empty response for message: %q", text)
		return
	}

	log.Printf("🤖 [AI] Response generated: %q", response)

	// Send response with typing presence.
	if err := sendTextWithPresenceToJID(ctx, h.messenger, replyTo, response); err != nil {
		log.Printf("❌ [SEND_FAIL] Failed to send response to %s: %v", replyTo, err)
	} else {
		log.Printf("📤 [SENT] Message successfully dispatched to %s", replyTo)
	}
}

// sendTextWithPresenceToJID handles both personal JIDs and group JIDs.
func sendTextWithPresenceToJID(ctx context.Context, m Messenger, target, text string) error {
	if m == nil || strings.TrimSpace(target) == "" {
		return nil
	}

	// Determine JID suffix.
	if !strings.Contains(target, "@") {
		target = target + "@s.whatsapp.net"
	}

	jid, err := types.ParseJID(target)
	if err != nil {
		return err
	}

	// Send presence (best-effort).
	_ = m.SendPresenceToJID(ctx, jid)

	return m.SendTextToJID(ctx, jid, text)
}

func getTextFromMessage(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}

	// 1) Regular conversation text.
	if t := msg.GetConversation(); t != "" {
		return t
	}

	// 2) Extended text (links/web client/quoted text containers).
	if t := msg.GetExtendedTextMessage(); t != nil {
		if text := t.GetText(); text != "" {
			return text
		}
	}

	// 3) Image caption.
	if t := msg.GetImageMessage(); t != nil {
		if caption := t.GetCaption(); caption != "" {
			return caption
		}
	}

	// 4) Video caption.
	if t := msg.GetVideoMessage(); t != nil {
		if caption := t.GetCaption(); caption != "" {
			return caption
		}
	}

	// 5) Ephemeral/disappearing messages.
	if t := msg.GetEphemeralMessage(); t != nil {
		return getTextFromMessage(t.GetMessage())
	}

	// 6) View-once messages.
	if t := msg.GetViewOnceMessage(); t != nil {
		return getTextFromMessage(t.GetMessage())
	}

	return ""
}
