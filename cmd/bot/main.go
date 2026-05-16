package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
	"github.com/sanspriyatna/wa-finance/internal/app"
	"github.com/sanspriyatna/wa-finance/internal/commands"
	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/finance"
	"github.com/sanspriyatna/wa-finance/internal/notes"
	"github.com/sanspriyatna/wa-finance/internal/reminder"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
	"github.com/sanspriyatna/wa-finance/internal/whatsapp"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func verifyLLMConnectivity(ctx context.Context, llmClient *ai.LLMClient) error {
	checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	const systemPrompt = "You are a healthcheck assistant. Reply with one short word only."
	resp, err := llmClient.Chat(checkCtx, systemPrompt, "reply with: READY")
	if err != nil {
		return fmt.Errorf("LLM connectivity check failed: %w", err)
	}
	if strings.TrimSpace(resp.Content) == "" && len(resp.ToolCalls) == 0 {
		return fmt.Errorf("LLM connectivity check failed: empty response")
	}
	return nil
}

func main() {
	// 1) Load config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}
	log.Println("✅ Config loaded")

	// 2) Connect WhatsApp
	waLogger := waLog.Stdout("WhatsApp", "ERROR", true)
	client, err := whatsapp.Connect(cfg.WASessionDBPath, waLogger)
	if err != nil {
		log.Fatalf("❌ WhatsApp connection error: %v", err)
	}
	log.Println("✅ WhatsApp connected")

	// 3) Initialize Google Sheets repository
	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets error: %v", err)
	}
	log.Println("✅ Google Sheets connected")

	ctx := context.Background()

	// 4) Full spreadsheet initialization
	log.Println("🔧 Menginisialisasi struktur spreadsheet...")
	if err := bootstrapSpreadsheet(ctx, repo); err != nil {
		log.Fatalf("❌ Gagal setup spreadsheet: %v", err)
	}
	log.Println("✅ Sheets tabs initialized")

	// 5) Initialize services
	llmClient := ai.NewLLMClient(cfg.LLMBaseURL, cfg.LLMApiKey, cfg.LLMModel)
	financeService := finance.NewFinanceService(repo, llmClient)
	notesService := notes.NewNotesService(repo)
	messenger := whatsapp.NewWhatsAppClient(client)
	var allRecipients []string
	allRecipients = append(allRecipients, cfg.OwnerIDs...)
	for _, gid := range cfg.AllowedGroupJIDs {
		if !strings.Contains(gid, "@") {
			allRecipients = append(allRecipients, gid+"@g.us")
		} else {
			allRecipients = append(allRecipients, gid)
		}
	}
	reminderService := reminder.NewService(repo, messenger, allRecipients, llmClient)

	// 6) LLM preflight (warn only, don't crash)
	if err := verifyLLMConnectivity(ctx, llmClient); err != nil {
		log.Printf("⚠️ LLM preflight check failed: %v. Bot tetap berjalan tanpa fitur AI.", err)
	} else {
		log.Println("✅ LLM preflight check passed")
	}

	// 7) Start reminder scheduler
	if err := reminderService.Start(ctx); err != nil {
		log.Fatalf("❌ Failed to start reminder service: %v", err)
	}
	log.Println("✅ Reminder scheduler started")

	// 8) Command router
	cmdRouter := commands.NewRouter()
	cmdRouter.Register("/start", commands.StartHandler)
	cmdRouter.Register("/help", commands.HelpHandler)
	cmdRouter.Register("/menu", commands.MenuHandler)
	cmdRouter.Register("/kategori", commands.CategoryHandler)
	cmdRouter.Register("/export", commands.NewExportHandlerFactory(cfg.SheetsID).Handler)
	cmdRouter.Register("/laporan", commands.NewReportHandlerFactory(financeService).Handler)
	cmdRouter.Register("/budget", commands.NewBudgetHandlerFactory(financeService).Handler)
	cmdRouter.Register("/notes", commands.NewNotesHandlerFactory(notesService).Handler)
	cmdRouter.Register("/edit", commands.NewEditHandlerFactory(financeService).Handler)
	cmdRouter.Register("/hapus", commands.NewDeleteHandlerFactory(financeService).Handler)
	cmdRouter.Register("/reminder", commands.NewReminderHandlerFactory(reminderService).Handler)
	cmdRouter.Register("/done", commands.NewDoneHandlerFactory(reminderService).Handler)

	// 9) App router
	appRouter := app.NewAppRouter(cmdRouter, llmClient, financeService, notesService, reminderService)
	appRouter.SetRepo(repo) // needed for undo + dashboard refresh

	// 10) WhatsApp message handler registration
	handler := whatsapp.NewHandler(messenger, cfg.OwnerIDs, appRouter.HandleMessage, cfg.AllowedGroupJIDs...)
	handler.Register(client)

	log.Println("✅ Bot is running! Waiting for messages...")

	// 11) Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("⏳ Shutting down...")
	reminderService.Stop()
	client.Disconnect()
	log.Println("✅ Bot stopped. Goodbye!")
}
