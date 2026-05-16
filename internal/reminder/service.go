package reminder

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

type Notifier interface {
	SendText(ctx context.Context, recipient string, text string) error
}

type Service struct {
	repo       sheets.SheetRepository
	notifier   Notifier
	recipients []string

	interval time.Duration
	now      func() time.Time

	reporter *scheduledReporter // proactive daily/weekly/monthly push

	stopCh  chan struct{}
	started atomic.Bool
	wg      sync.WaitGroup
}

type ParsedReminder struct {
	TargetDate      time.Time
	TargetTime      string
	Message         string
	Mode            sheets.ReminderMode
	RemindersPerDay int
}

var (
	reDateTag     = regexp.MustCompile(`(?i)\b(?:tgl|tanggal)\s+(\d{1,2})\s+([a-zA-Z]+)(?:\s+(\d{4}))?\b`)
	reDateRaw     = regexp.MustCompile(`(?i)\b(\d{1,2})\s+([a-zA-Z]+)(?:\s+(\d{4}))?\b`)
	reTimeTag     = regexp.MustCompile(`(?i)\b(?:jam|pukul)\s+(\d{1,2})(?:[:.](\d{2}))?\b`)
	reTimeRaw     = regexp.MustCompile(`\b(\d{1,2})[:.](\d{2})\b`)
	reSpaces      = regexp.MustCompile(`\s+`)
	reNonWordLike = regexp.MustCompile(`[^a-z0-9\s]+`)
)

var idCounter atomic.Uint64

func NewService(repo sheets.SheetRepository, notifier Notifier, recipients []string, aiClient *ai.LLMClient) *Service {
	nowFn := func() time.Time { return time.Now().In(sheets.WIB) }
	return &Service{
		repo:       repo,
		notifier:   notifier,
		recipients: recipients,
		interval:   time.Minute,
		now:        nowFn,
		reporter:   newScheduledReporter(notifier, repo, recipients, nowFn, aiClient),
		stopCh:     make(chan struct{}),
	}
}

func (s *Service) SetInterval(interval time.Duration) {
	if interval <= 0 {
		return
	}
	s.interval = interval
}

func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("reminder service is nil")
	}
	if s.repo == nil {
		return fmt.Errorf("sheet repository is nil")
	}
	if s.notifier == nil {
		return fmt.Errorf("notifier is nil")
	}
	if len(s.recipients) == 0 {
		return fmt.Errorf("recipients list is empty")
	}
	if s.started.Swap(true) {
		return nil
	}

	if err := s.repo.InitReminderTab(ctx); err != nil {
		s.started.Store(false)
		return fmt.Errorf("failed to init reminder tab: %w", err)
	}

	s.wg.Add(1)
	go s.loop()
	return nil
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	if !s.started.Load() {
		return
	}
	close(s.stopCh)
	s.wg.Wait()
	s.started.Store(false)
}

func (s *Service) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run once immediately so reminders don't wait one full interval.
	_, _ = s.ProcessDueReminders(context.Background())

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()
			_, _ = s.ProcessDueReminders(ctx)
			if s.reporter != nil {
				s.reporter.Tick(ctx)
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *Service) CreateFromText(ctx context.Context, sender string, text string) (*sheets.Reminder, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("reminder service is not ready")
	}
	parsed, err := ParseReminderText(text, s.now())
	if err != nil {
		return nil, err
	}

	rem := &sheets.Reminder{
		ID:              s.nextReminderID(),
		Message:         parsed.Message,
		TargetDate:      parsed.TargetDate,
		TargetTime:      parsed.TargetTime,
		Mode:            parsed.Mode,
		RemindersPerDay: parsed.RemindersPerDay,
		Status:          sheets.ReminderStatusActive,
		CreatedAt:       s.now(),
		UpdatedAt:       s.now(),
		TargetJID:       strings.TrimSpace(sender),
	}
	if rem.TargetJID == s.recipients[0] {
		rem.TargetJID = ""
	}
	rem.Normalize()

	if err := rem.Validate(); err != nil {
		return nil, err
	}

	if err := s.repo.AppendReminder(ctx, rem); err != nil {
		return nil, fmt.Errorf("failed to save reminder: %w", err)
	}

	return rem, nil
}

func (s *Service) CompleteByID(ctx context.Context, id string, note string) (*sheets.Reminder, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("reminder service is not ready")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("reminder ID is required")
	}

	rem, rowIndex, err := s.repo.GetReminderByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if rem.IsCompleted() {
		return rem, nil
	}

	rem.MarkCompleted(s.now())
	if strings.TrimSpace(note) != "" {
		rem.Notes = strings.TrimSpace(note)
	}
	rem.UpdatedAt = s.now()

	if err := s.repo.UpdateReminder(ctx, rowIndex, rem); err != nil {
		return nil, fmt.Errorf("failed to mark reminder as completed: %w", err)
	}
	return rem, nil
}

// TryCompleteFromText tries to mark a reminder as completed from natural chat text.
// Example accepted intents:
// - "gw udah cuci mobil"
// - "sudah bayar vps"
// - "beres urus pajak"
//
// Returns:
// - reminder: completed reminder when matched
// - matched: whether the text looked like a completion intent and was matched
// - error: operational error (repo/update/etc)
func (s *Service) TryCompleteFromText(ctx context.Context, text string) (*sheets.Reminder, bool, error) {
	if s == nil || s.repo == nil {
		return nil, false, fmt.Errorf("reminder service is not ready")
	}

	intentText, ok := extractCompletionIntent(text)
	if !ok {
		return nil, false, nil
	}

	active, err := s.repo.ListActiveReminders(ctx)
	if err != nil {
		return nil, true, fmt.Errorf("failed to list active reminders: %w", err)
	}
	if len(active) == 0 {
		return nil, true, nil
	}

	best := chooseBestReminder(intentText, active)
	if best == nil {
		return nil, true, nil
	}

	note := "auto completed from chat: " + strings.TrimSpace(text)
	done, err := s.CompleteByID(ctx, best.ID, note)
	if err != nil {
		return nil, true, err
	}
	return done, true, nil
}

func extractCompletionIntent(text string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(text))
	if raw == "" {
		return "", false
	}

	prefixes := []string{
		"gw udah",
		"gue udah",
		"aku udah",
		"saya udah",
		"sudah",
		"udah",
		"beres",
		"selesai",
		"kelar",
		"done",
		"sudah kelar",
		"udah kelar",
	}

	for _, p := range prefixes {
		if strings.HasPrefix(raw, p) {
			intent := strings.TrimSpace(strings.TrimPrefix(raw, p))
			intent = normalizeIntentText(intent)
			if intent == "" {
				return "", false
			}
			return intent, true
		}
	}
	return "", false
}

func chooseBestReminder(intent string, reminders []sheets.Reminder) *sheets.Reminder {
	intentTokens := tokenSet(intent)
	if len(intentTokens) == 0 {
		return nil
	}

	var best *sheets.Reminder
	bestScore := 0.0

	for i := range reminders {
		r := reminders[i]
		if r.Status != sheets.ReminderStatusActive {
			continue
		}

		msg := normalizeIntentText(r.Message)
		msgTokens := tokenSet(msg)
		if len(msgTokens) == 0 {
			continue
		}

		score := overlapScore(intentTokens, msgTokens)
		if score <= 0 {
			continue
		}

		// Prefer reminders with target date closest to now (small bias)
		// so recent/near reminders win on tie.
		if score > bestScore {
			bestScore = score
			best = &reminders[i]
		}
	}

	// Minimum confidence threshold to avoid wrong completion.
	// 0.34 ~= at least one-third token overlap.
	if bestScore < 0.34 {
		return nil
	}
	return best
}

func normalizeIntentText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = reNonWordLike.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func tokenSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, t := range strings.Fields(s) {
		// drop tiny/noisy tokens
		if len(t) <= 2 {
			continue
		}
		out[t] = struct{}{}
	}
	return out
}

func overlapScore(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}

	// Dice coefficient: 2*|A∩B| / (|A|+|B|)
	return (2.0 * float64(inter)) / float64(len(a)+len(b))
}

func (s *Service) ProcessDueReminders(ctx context.Context) (int, error) {
	if s == nil || s.repo == nil || s.notifier == nil {
		return 0, fmt.Errorf("reminder service is not ready")
	}

	now := s.now()
	due, err := s.repo.ListDueReminders(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("failed to list due reminders: %w", err)
	}

	sent := 0
	for _, item := range due {
		rem := item // copy
		msg := formatReminderPing(rem, now)

		targetSender := s.recipients[0]
		if rem.TargetJID != "" {
			targetSender = rem.TargetJID
		}

		sendErr := s.notifier.SendText(ctx, targetSender, msg)

		// Re-load latest row before update.
		current, rowIndex, getErr := s.repo.GetReminderByID(ctx, rem.ID)
		if getErr != nil {
			continue
		}
		if current == nil || current.Status != sheets.ReminderStatusActive {
			continue
		}

		if sendErr != nil {
			current.UpdatedAt = now
			current.Notes = strings.TrimSpace(current.Notes + " | send_error: " + sendErr.Error())
			_ = s.repo.UpdateReminder(ctx, rowIndex, current)
			continue
		}

		current.MarkReminded(now)

		if current.Mode == sheets.ReminderModeOnce {
			current.MarkCompleted(now)
		}
		current.UpdatedAt = now

		if err := s.repo.UpdateReminder(ctx, rowIndex, current); err != nil {
			continue
		}
		sent++
	}

	return sent, nil
}

func ParseReminderText(input string, now time.Time) (*ParsedReminder, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, fmt.Errorf("teks reminder kosong")
	}

	lower := strings.ToLower(raw)
	targetDate := now.In(sheets.WIB)

	// Date parsing.
	consumedDate := ""
	if strings.Contains(lower, "besok") {
		targetDate = targetDate.AddDate(0, 0, 1)
		consumedDate = "besok"
	} else if m := reDateTag.FindStringSubmatch(raw); len(m) > 0 {
		day, month, year, err := parseDateParts(m[1], m[2], m[3], targetDate.Year())
		if err != nil {
			return nil, err
		}
		targetDate = time.Date(year, month, day, 0, 0, 0, 0, sheets.WIB)
		consumedDate = m[0]
	} else if m := reDateRaw.FindStringSubmatch(raw); len(m) > 0 {
		if monthFromName(strings.ToLower(m[2])) != time.Month(0) {
			day, month, year, err := parseDateParts(m[1], m[2], m[3], targetDate.Year())
			if err != nil {
				return nil, err
			}
			targetDate = time.Date(year, month, day, 0, 0, 0, 0, sheets.WIB)
			consumedDate = m[0]
		}
	}

	// Time parsing.
	targetTime := ""
	consumedTime := ""
	if m := reTimeTag.FindStringSubmatch(raw); len(m) > 0 {
		hour, _ := strconv.Atoi(m[1])
		min := 0
		if m[2] != "" {
			min, _ = strconv.Atoi(m[2])
		}
		if hour >= 0 && hour <= 23 && min >= 0 && min <= 59 {
			targetTime = fmt.Sprintf("%02d:%02d", hour, min)
			consumedTime = m[0]
		}
	} else if m := reTimeRaw.FindStringSubmatch(raw); len(m) > 0 {
		hour, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if hour >= 0 && hour <= 23 && min >= 0 && min <= 59 {
			targetTime = fmt.Sprintf("%02d:%02d", hour, min)
			consumedTime = m[0]
		}
	}

	msg := cleanupReminderMessage(raw, consumedDate, consumedTime)
	if msg == "" {
		return nil, fmt.Errorf("pesan reminder tidak ditemukan")
	}

	mode := sheets.ReminderModeOnce
	perDay := 1

	// User requirement:
	// If no explicit time -> remind 3x/day until user marks done.
	if targetTime == "" {
		mode = sheets.ReminderModeUntilDone
		perDay = 3
	}

	return &ParsedReminder{
		TargetDate:      targetDate,
		TargetTime:      targetTime,
		Message:         msg,
		Mode:            mode,
		RemindersPerDay: perDay,
	}, nil
}

func cleanupReminderMessage(raw string, consumedDate string, consumedTime string) string {
	out := raw

	// Remove known prefaces.
	prefixes := []string{
		"ingetin dong",
		"ingetin",
		"ingatkan dong",
		"ingatkan",
		"reminder",
		"tolong",
	}
	lower := strings.ToLower(out)
	for _, p := range prefixes {
		if strings.HasPrefix(strings.TrimSpace(lower), p) {
			idx := strings.Index(strings.ToLower(out), p)
			if idx >= 0 {
				out = strings.TrimSpace(out[:idx] + out[idx+len(p):])
				lower = strings.ToLower(out)
			}
		}
	}

	if consumedDate != "" {
		out = strings.ReplaceAll(out, consumedDate, "")
		out = strings.ReplaceAll(strings.ToLower(out), strings.ToLower(consumedDate), "")
	}
	if consumedTime != "" {
		out = strings.ReplaceAll(out, consumedTime, "")
		out = strings.ReplaceAll(strings.ToLower(out), strings.ToLower(consumedTime), "")
	}

	// Remove leftover date keywords.
	out = regexp.MustCompile(`(?i)\b(?:tgl|tanggal|besok|jam|pukul)\b`).ReplaceAllString(out, " ")
	out = reSpaces.ReplaceAllString(out, " ")
	out = strings.TrimSpace(out)

	// Clean punctuation on both ends.
	out = strings.Trim(out, ",.-:; ")

	// Make sentence style.
	if out == "" {
		return ""
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

func parseDateParts(dayRaw, monthRaw, yearRaw string, fallbackYear int) (int, time.Month, int, error) {
	day, err := strconv.Atoi(strings.TrimSpace(dayRaw))
	if err != nil || day < 1 || day > 31 {
		return 0, 0, 0, fmt.Errorf("tanggal tidak valid")
	}
	month := monthFromName(strings.ToLower(strings.TrimSpace(monthRaw)))
	if month == time.Month(0) {
		return 0, 0, 0, fmt.Errorf("bulan tidak dikenal: %s", monthRaw)
	}

	year := fallbackYear
	if strings.TrimSpace(yearRaw) != "" {
		y, err := strconv.Atoi(strings.TrimSpace(yearRaw))
		if err != nil || y < 2000 || y > 2100 {
			return 0, 0, 0, fmt.Errorf("tahun tidak valid")
		}
		year = y
	}

	return day, month, year, nil
}

func monthFromName(name string) time.Month {
	months := map[string]time.Month{
		"januari":   time.January,
		"februari":  time.February,
		"maret":     time.March,
		"april":     time.April,
		"mei":       time.May,
		"juni":      time.June,
		"juli":      time.July,
		"agustus":   time.August,
		"september": time.September,
		"oktober":   time.October,
		"november":  time.November,
		"desember":  time.December,
	}
	return months[name]
}

func formatReminderPing(rem sheets.Reminder, now time.Time) string {
	targetDate := rem.TargetDate.In(sheets.WIB).Format("02 Jan 2006")
	when := targetDate
	if rem.TargetTime != "" {
		when += " " + rem.TargetTime + " WIB"
	} else {
		when += " (3x sehari)"
	}

	return fmt.Sprintf(
		"🔔 *WAKTUNYA:* %s\n\n🆔 ID: %s\n🗓️ Target: %s\n\nJika sudah dilakukan, ketik: */done %s* ✅",
		rem.Message,
		rem.ID,
		when,
		rem.ID,
	)
}

func (s *Service) nextReminderID() string {
	now := s.now().Format("20060102")
	n := idCounter.Add(1) % 1000
	return fmt.Sprintf("RMD-%s-%03d", now, n)
}
