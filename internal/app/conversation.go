package app

import (
	"sync"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
)

// conversationStore keeps a sliding-window history of ai.Message entries per
// sender so the LLM has multi-turn context. It is safe for concurrent use.
//
// Design decisions:
//   - Window size: maxTurns pairs (user + assistant) = 2*maxTurns messages.
//     The system message is always prepended at call time and is NOT stored.
//   - TTL: sessions inactive for longer than ttl are automatically pruned.
//   - Tool results: when the AI triggers a tool call the assistant "turn" is
//     stored as the formatted bot response text so subsequent turns have context
//     about what was just done ("transaksi tercatat", etc.).
type conversationStore struct {
	mu       sync.Mutex
	sessions map[string]*session
	maxTurns int           // max user+assistant pairs kept
	ttl      time.Duration // session inactivity timeout
}

type session struct {
	entries  []ai.Message
	lastSeen time.Time
}

const (
	defaultMaxTurns = 8               // 8 pairs = 16 messages (plus system)
	defaultTTL      = 30 * time.Minute
)

func newConversationStore() *conversationStore {
	return &conversationStore{
		sessions: make(map[string]*session),
		maxTurns: defaultMaxTurns,
		ttl:      defaultTTL,
	}
}

// getHistory returns a copy of the stored history for sender.
// Expired sessions are silently pruned and return an empty slice.
func (s *conversationStore) getHistory(sender string) []ai.Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sender]
	if !ok {
		return nil
	}
	if time.Since(sess.lastSeen) > s.ttl {
		delete(s.sessions, sender)
		return nil
	}

	// Return a shallow copy so callers cannot mutate internal state.
	out := make([]ai.Message, len(sess.entries))
	copy(out, sess.entries)
	return out
}

// addTurn appends a user message and the corresponding assistant reply to the
// session, then enforces the sliding-window size.
func (s *conversationStore) addTurn(sender, userText, assistantText string) {
	if sender == "" || userText == "" || assistantText == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sender]
	if !ok {
		sess = &session{}
		s.sessions[sender] = sess
	}

	sess.entries = append(sess.entries,
		ai.Message{Role: "user", Content: userText},
		ai.Message{Role: "assistant", Content: assistantText},
	)
	sess.lastSeen = time.Now()

	// Trim to window: keep last maxTurns pairs (= 2*maxTurns messages).
	maxLen := s.maxTurns * 2
	if len(sess.entries) > maxLen {
		sess.entries = sess.entries[len(sess.entries)-maxLen:]
	}
}

// clear removes a sender's entire history (e.g. on /start command).
func (s *conversationStore) clear(sender string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sender)
}

// pruneExpired removes all sessions that have been inactive longer than ttl.
// Call this periodically if needed (currently not scheduled; OK for low traffic).
func (s *conversationStore) pruneExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sess := range s.sessions {
		if time.Since(sess.lastSeen) > s.ttl {
			delete(s.sessions, k)
		}
	}
}
