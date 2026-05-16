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

	ctx := context.Background()
	srv, err := sheets.NewService(ctx, option.WithCredentialsFile(credsPath))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	tabName := "Mei 2026"
	readRange := fmt.Sprintf("'%s'!A:G", tabName)
	resp, err := srv.Spreadsheets.Values.Get(spreadsheetID, readRange).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve data from sheet: %v", err)
	}

	if len(resp.Values) == 0 {
		fmt.Println("No data found.")
	} else {
		fmt.Println("ID, Date, Time, Type, Category, Description, Amount")
		for _, row := range resp.Values {
			fmt.Printf("%v\n", row)
		}
	}
}
