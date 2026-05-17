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
	_ = godotenv.Load("../.env") // check parent directory as well
	_ = godotenv.Load()          // check current directory

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

	readRange := "'Reminders'!A2:M"
	resp, err := srv.Spreadsheets.Values.Get(spreadsheetID, readRange).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from Reminders sheet: %v", err)
	}

	if len(resp.Values) == 0 {
		fmt.Println("No reminders found in 'Reminders' tab.")
		return
	}

	fmt.Println("=========================================================================")
	fmt.Printf("%-10s | %-12s | %-12s | %-10s | %s\n", "Row Index", "ID", "Date", "Status", "Message")
	fmt.Println("=========================================================================")
	for i, row := range resp.Values {
		rowIndex := i + 2
		id := ""
		date := ""
		status := ""
		msg := ""
		if len(row) > 0 {
			id = fmt.Sprintf("%v", row[0])
		}
		if len(row) > 1 {
			date = fmt.Sprintf("%v", row[1])
		}
		if len(row) > 6 {
			status = fmt.Sprintf("%v", row[6])
		}
		if len(row) > 3 {
			msg = fmt.Sprintf("%v", row[3])
		}
		fmt.Printf("Row %-4d | %-12s | %-12s | %-10s | %s\n", rowIndex, id, date, status, msg)
	}
	fmt.Println("=========================================================================")
}
