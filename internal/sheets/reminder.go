package sheets

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ReminderMode defines delivery behavior.
type ReminderMode string

const (
	// ReminderModeOnce sends reminder one time at/after target time.
	ReminderModeOnce ReminderMode = "once"
	// ReminderModeUntilDone keeps reminding user until manually completed.
	ReminderModeUntilDone ReminderMode = "until_done"
)

// ReminderStatus defines reminder lifecycle state.
type ReminderStatus string

const (
	ReminderStatusActive    ReminderStatus = "active"
	ReminderStatusCompleted ReminderStatus = "completed"
	ReminderStatusPaused    ReminderStatus = "paused"
)

// ReminderSheetName is the default sheet tab for reminders.
const ReminderSheetName = "Reminders"

// ReminderHeaders defines canonical reminder sheet columns.
var ReminderHeaders = []interface{}{
	"ID",
	"Tanggal Target",
	"Waktu Target",
	"Pesan",
	"Mode",
	"Pengingat/Hari",
	"Status",
	"Dibuat Tanggal",
	"Dibuat Waktu",
	"Diubah Tanggal",
	"Diubah Waktu",
	"Last Reminder Date",
}

// Reminder supports:
// 1) time-specific reminder (Waktu Target set) and
// 2) no-time recurring reminder (Waktu Target empty, mode until_done, reminders/day > 0).
type Reminder struct {
	ID      string
	Message string

	// TargetDate is required (WIB date).
	TargetDate time.Time
	// TargetTime format: "15:04". Empty means no specific time.
	TargetTime string

	Mode             ReminderMode
	RemindersPerDay  int
	Status           ReminderStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastReminderDate string // YYYYMMDD (WIB)
	RemindedToday    int

	CompletedAt *time.Time
	Notes       string
	TargetJID   string
}

// Normalize applies defaults and trims values.
func (r *Reminder) Normalize() {
	if r == nil {
		return
	}

	r.ID = strings.TrimSpace(r.ID)
	r.Message = strings.TrimSpace(r.Message)
	r.TargetTime = strings.TrimSpace(r.TargetTime)
	r.Notes = strings.TrimSpace(r.Notes)
	r.TargetJID = strings.TrimSpace(r.TargetJID)
	r.LastReminderDate = strings.TrimSpace(r.LastReminderDate)

	now := time.Now().In(WIB)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}

	if r.Mode == "" {
		if r.TargetTime == "" {
			r.Mode = ReminderModeUntilDone
		} else {
			r.Mode = ReminderModeOnce
		}
	}

	if r.RemindersPerDay <= 0 {
		if r.Mode == ReminderModeUntilDone && r.TargetTime == "" {
			r.RemindersPerDay = 3 // default for no-time recurring reminders
		} else {
			r.RemindersPerDay = 1
		}
	}

	if r.Status == "" {
		r.Status = ReminderStatusActive
	}

	// Completed reminder always keeps completed timestamp.
	if r.Status == ReminderStatusCompleted && r.CompletedAt == nil {
		t := now
		r.CompletedAt = &t
	}
}

// Validate ensures reminder data is consistent.
func (r *Reminder) Validate() error {
	if r == nil {
		return fmt.Errorf("reminder is nil")
	}

	r.Normalize()

	if r.ID == "" {
		return fmt.Errorf("reminder ID is required")
	}
	if r.Message == "" {
		return fmt.Errorf("reminder message is required")
	}
	if r.TargetDate.IsZero() {
		return fmt.Errorf("target date is required")
	}

	if r.TargetTime != "" {
		if _, err := time.Parse("15:04", r.TargetTime); err != nil {
			return fmt.Errorf("invalid target time %q (expected HH:MM): %w", r.TargetTime, err)
		}
	}

	switch r.Mode {
	case ReminderModeOnce, ReminderModeUntilDone:
	default:
		return fmt.Errorf("invalid reminder mode: %s", r.Mode)
	}

	switch r.Status {
	case ReminderStatusActive, ReminderStatusCompleted, ReminderStatusPaused:
	default:
		return fmt.Errorf("invalid reminder status: %s", r.Status)
	}

	if r.RemindersPerDay <= 0 {
		return fmt.Errorf("reminders/day must be > 0")
	}

	return nil
}

// IsCompleted returns true if reminder has been manually completed.
func (r *Reminder) IsCompleted() bool {
	return r != nil && r.Status == ReminderStatusCompleted
}

// MarkCompleted marks reminder as done by user.
func (r *Reminder) MarkCompleted(now time.Time) {
	if r == nil {
		return
	}
	n := now.In(WIB)
	r.Status = ReminderStatusCompleted
	r.CompletedAt = &n
	r.UpdatedAt = n
}

// MarkReminded increments daily counter for recurring reminders.
func (r *Reminder) MarkReminded(now time.Time) {
	if r == nil {
		return
	}

	n := now.In(WIB)
	day := n.Format("20060102")
	if r.LastReminderDate != day {
		r.LastReminderDate = day
		r.RemindedToday = 0
	}
	r.RemindedToday++
	r.UpdatedAt = n
}

// CanSendNow returns whether reminder is eligible to be sent at current time.
func (r *Reminder) CanSendNow(now time.Time) bool {
	if r == nil {
		return false
	}
	if r.Status != ReminderStatusActive {
		return false
	}

	n := now.In(WIB)
	targetDay := r.TargetDate.In(WIB).Format("20060102")
	currentDay := n.Format("20060102")
	if currentDay < targetDay {
		return false
	}

	remindedToday := effectiveDailyReminderCount(r.LastReminderDate, r.RemindedToday, n)
	nowMinute := n.Hour()*60 + n.Minute()

	// Time-specific reminder:
	if r.TargetTime != "" {
		targetDateTime, err := time.ParseInLocation(
			"02/01/2006 15:04",
			r.TargetDate.In(WIB).Format("02/01/2006")+" "+r.TargetTime,
			WIB,
		)
		if err != nil {
			return false
		}
		if n.Before(targetDateTime) {
			return false
		}

		// once: send only one time
		if r.Mode == ReminderModeOnce {
			return r.LastReminderDate == ""
		}

		// until_done: use daily windows starting from target time to avoid spam.
		startMinute, ok := parseClockToMinutes(r.TargetTime)
		if !ok {
			return false
		}
		windows := dailyReminderWindows(r.RemindersPerDay, startMinute)
		if len(windows) == 0 || remindedToday >= len(windows) {
			return false
		}
		return nowMinute >= windows[remindedToday]
	}

	// No specific time:
	if r.Mode == ReminderModeOnce {
		// one send on/after target date
		return r.LastReminderDate == ""
	}

	// No-time recurring until done: use scheduled daily windows (default 3x/day).
	windows := dailyReminderWindows(r.RemindersPerDay, 9*60)
	if len(windows) == 0 || remindedToday >= len(windows) {
		return false
	}
	return nowMinute >= windows[remindedToday]
}

// ToRow converts reminder to sheet row.
func (r *Reminder) ToRow() []interface{} {
	if r == nil {
		return []interface{}{}
	}
	r.Normalize()

	target := r.TargetDate.In(WIB)
	created := r.CreatedAt.In(WIB)
	updated := r.UpdatedAt.In(WIB)

	lastReminderState := r.LastReminderDate
	if r.RemindedToday > 0 {
		lastReminderState = fmt.Sprintf("%s:%d", r.LastReminderDate, r.RemindedToday)
	}

	// Prefix times with apostrophe to force Google Sheets text mode
	// (prevents auto-conversion of "00:47" to a time fraction).
	forceText := func(s string) string {
		if s == "" {
			return s
		}
		return "'" + s
	}

	return []interface{}{
		r.ID,
		target.Format("02/01/2006"),
		forceText(r.TargetTime),
		r.Message,
		string(r.Mode),
		r.RemindersPerDay,
		string(r.Status),
		created.Format("02/01/2006"),
		forceText(created.Format("15:04")),
		updated.Format("02/01/2006"),
		forceText(updated.Format("15:04")),
		lastReminderState,
		r.TargetJID,
	}
}

// ReminderFromRow parses row into Reminder.
//
// Supported row format is aligned with ReminderHeaders.
// It also tolerates shorter rows by applying defaults.
func ReminderFromRow(row []interface{}) (*Reminder, error) {
	if len(row) < 4 {
		return nil, fmt.Errorf("invalid reminder row: expected at least 4 columns, got %d", len(row))
	}

	id := cellString(row[0])
	targetDateStr := cellString(row[1])
	targetTime := ""
	if len(row) > 2 {
		targetTime = cellString(row[2])
	}
	message := ""
	if len(row) > 3 {
		message = cellString(row[3])
	}

	lastReminderState := ""
	if len(row) > 11 {
		lastReminderState = strings.TrimSpace(cellString(row[11]))
	}
	targetJID := ""
	if len(row) > 12 {
		targetJID = cellString(row[12])
	}

	if id == "" {
		return nil, fmt.Errorf("invalid reminder row: missing ID")
	}
	if message == "" {
		return nil, fmt.Errorf("invalid reminder row: missing message")
	}

	targetDate, err := time.ParseInLocation("02/01/2006", targetDateStr, WIB)
	if err != nil {
		return nil, fmt.Errorf("invalid target date: %w", err)
	}

	mode := ReminderModeOnce
	if len(row) > 4 {
		m := ReminderMode(strings.TrimSpace(strings.ToLower(cellString(row[4]))))
		if m != "" {
			mode = m
		}
	}

	remindersPerDay := 1
	if len(row) > 5 {
		if v, err := cellInt(row[5]); err == nil && v > 0 {
			remindersPerDay = v
		}
	}

	status := ReminderStatusActive
	if len(row) > 6 {
		s := ReminderStatus(strings.TrimSpace(strings.ToLower(cellString(row[6]))))
		if s != "" {
			status = s
		}
	}

	createdAt := time.Now().In(WIB)
	if len(row) > 8 {
		createdDate := cellString(row[7])
		createdTime := cellString(row[8])
		if createdDate != "" && createdTime != "" {
			if t, err := parseDateTimePair(createdDate, createdTime); err == nil {
				createdAt = t
			}
		}
	}

	updatedAt := createdAt
	if len(row) > 10 {
		updatedDate := cellString(row[9])
		updatedTime := cellString(row[10])
		if updatedDate != "" && updatedTime != "" {
			if t, err := parseDateTimePair(updatedDate, updatedTime); err == nil {
				updatedAt = t
			}
		}
	}

	lastReminderDate := ""
	remindedToday := 0
	if lastReminderState != "" {
		parts := strings.Split(lastReminderState, ":")
		lastReminderDate = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && v >= 0 {
				remindedToday = v
			}
		}
	}

	var completedAt *time.Time
	notes := ""

	rem := &Reminder{
		ID:               id,
		Message:          message,
		TargetDate:       targetDate,
		TargetTime:       targetTime,
		Mode:             mode,
		RemindersPerDay:  remindersPerDay,
		Status:           status,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		LastReminderDate: lastReminderDate,
		RemindedToday:    remindedToday,
		CompletedAt:      completedAt,
		Notes:            notes,
		TargetJID:        targetJID,
	}
	rem.Normalize()

	if err := rem.Validate(); err != nil {
		return nil, err
	}

	return rem, nil
}

func parseDateTimePair(datePart, timePart string) (time.Time, error) {
	datePart = strings.TrimSpace(datePart)
	timePart = strings.TrimSpace(timePart)
	if datePart == "" || timePart == "" {
		return time.Time{}, fmt.Errorf("date/time is empty")
	}
	return time.ParseInLocation("02/01/2006 15:04", datePart+" "+timePart, WIB)
}

func effectiveDailyReminderCount(lastReminderDate string, remindedToday int, now time.Time) int {
	if remindedToday < 0 {
		remindedToday = 0
	}
	if strings.TrimSpace(lastReminderDate) == now.In(WIB).Format("20060102") {
		return remindedToday
	}
	return 0
}

func parseClockToMinutes(hhmm string) (int, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(hhmm))
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
}

func dailyReminderWindows(remindersPerDay int, startMinute int) []int {
	if remindersPerDay <= 0 {
		return nil
	}

	if startMinute < 0 {
		startMinute = 0
	}
	if startMinute > 22*60 {
		startMinute = 22 * 60
	}

	switch remindersPerDay {
	case 1:
		if startMinute < 9*60 {
			return []int{9 * 60}
		}
		return []int{startMinute}

	case 2:
		first := 9 * 60
		second := 18 * 60
		if startMinute > first {
			first = startMinute
		}
		if second < first {
			second = first
		}
		return []int{first, second}

	case 3:
		out := []int{9 * 60, 14 * 60, 20 * 60}
		if startMinute > out[0] {
			out[0] = startMinute
		}
		for i := 1; i < len(out); i++ {
			if out[i] < out[i-1] {
				out[i] = out[i-1]
			}
		}
		return out

	default:
		endMinute := 22 * 60
		if startMinute >= endMinute {
			startMinute = endMinute - 1
		}

		span := endMinute - startMinute
		if span < remindersPerDay-1 {
			span = remindersPerDay - 1
		}
		step := span / (remindersPerDay - 1)
		if step < 1 {
			step = 1
		}

		out := make([]int, 0, remindersPerDay)
		current := startMinute
		for i := 0; i < remindersPerDay; i++ {
			if current > endMinute {
				current = endMinute
			}
			out = append(out, current)
			current += step
		}
		return out
	}
}

func cellInt(v interface{}) (int, error) {
	s := strings.TrimSpace(cellString(v))
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}
