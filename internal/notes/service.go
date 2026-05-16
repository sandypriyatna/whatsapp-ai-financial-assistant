package notes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

type NotesService struct {
	repo sheets.SheetRepository
}

func NewNotesService(repo sheets.SheetRepository) *NotesService {
	return &NotesService{repo: repo}
}

func (s *NotesService) SaveNote(ctx context.Context, content string) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("sheet repository is nil")
	}

	noteText := strings.TrimSpace(content)
	if noteText == "" {
		return fmt.Errorf("catatan tidak boleh kosong")
	}

	if err := s.repo.EnsureTabExists(ctx, "Notes"); err != nil {
		return fmt.Errorf("failed to ensure Notes tab: %w", err)
	}

	note := &sheets.Note{
		Date:    nowWIB(),
		Content: noteText,
	}

	if err := s.repo.AppendNote(ctx, note); err != nil {
		return fmt.Errorf("failed to append note: %w", err)
	}

	return nil
}

func nowWIB() time.Time {
	return time.Now().In(sheets.WIB)
}
