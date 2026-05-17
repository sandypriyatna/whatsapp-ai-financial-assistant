package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

func main() {
	_ = godotenv.Load()

	spreadsheetID := os.Getenv("SHEETS_SPREADSHEET_ID")
	credsPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")

	if spreadsheetID == "" {
		spreadsheetID = "1HlCM4YwFJ2KYRpxGomIOiWaMyqhK5yMk6pMdkpqBWIQ"
	}
	if credsPath == "" {
		credsPath = "./data/google/wa-finance-bot-496515-24773bae45fb.json"
	}

	ctx := context.Background()
	srv, err := sheets.NewService(ctx, option.WithCredentialsFile(credsPath))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	clearRange := "'Reminders'!A2:Z1000"
	_, err = srv.Spreadsheets.Values.Clear(spreadsheetID, clearRange, &sheets.ClearValuesRequest{}).Do()
	if err != nil {
		log.Fatalf("Failed to clear Reminders: %v", err)
	}

	fmt.Println("✅ Successfully cleared all reminders (row 2 onwards) from 'Reminders' tab!")
}
