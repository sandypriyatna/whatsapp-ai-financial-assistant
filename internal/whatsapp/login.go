package whatsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"database/sql"
	"modernc.org/sqlite"
)

func init() {
	sql.Register("sqlite3", &sqlite.Driver{})
}

// Connect initializes WhatsApp client with sqlite-backed session persistence.
//
// Behavior:
// - Opens/creates sqlite store at dbPath
// - Reuses existing session if present
// - Shows QR in terminal for first-time login
// - Enables auto-reconnect
func Connect(dbPath string, pairingPhone string, log waLog.Logger) (*whatsmeow.Client, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("dbPath is required")
	}

	// Ensure parent directory exists.
	if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create session db directory: %w", err)
		}
	}

	ctx := context.Background()

	container, err := sqlstore.New(
		ctx,
		"sqlite3",
		"file:"+dbPath+"?_pragma=foreign_keys(1)&_busy_timeout=5000",
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize sqlstore: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, log)
	client.EnableAutoReconnect = true

	// First-time login requires QR scan or pairing code.
	if client.Store.ID == nil {
		if pairingPhone != "" {
			if err := client.Connect(); err != nil {
				return nil, fmt.Errorf("failed to connect for pairing: %w", err)
			}
			// Wait a few seconds for the connection to stabilize before requesting the code.
			fmt.Println("⏳ Menghubungkan ke WhatsApp...")
			time.Sleep(3 * time.Second)
			code, err := client.PairPhone(ctx, pairingPhone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
			if err != nil {
				return nil, fmt.Errorf("failed to get pairing code: %w", err)
			}
			fmt.Printf("\n🔑 PAIRING CODE: %s\n", code)
			fmt.Println("Buka WhatsApp HP -> Settings -> Linked Devices -> Link with phone number instead")
			fmt.Println("Masukkan kode di atas.")
			fmt.Println("Menunggu pairing selesai...")
			return client, nil
		}

		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create QR channel: %w", err)
		}

		if err := client.Connect(); err != nil {
			return nil, fmt.Errorf("failed to connect WhatsApp client: %w", err)
		}

		fmt.Println("📱 Scan QR code berikut di WhatsApp (Linked Devices):")
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				fmt.Println("\nJika QR terpotong (mode SSH/headless), gunakan teks berikut:")
				fmt.Println("→ Salin teks di bawah ke https://qr.io (pilih 'Text') lalu scan dari HP:")
				fmt.Println("\n" + evt.Code + "\n")
				fmt.Println("Menunggu scan QR di WhatsApp (Settings → Linked Devices → Link a Device)...")

			case "success":
				fmt.Println("✅ Pairing berhasil.")
				return client, nil

			case "timeout":
				client.Disconnect()
				return nil, fmt.Errorf("QR code timed out, please restart")

			case "error":
				client.Disconnect()
				if evt.Error != nil {
					return nil, fmt.Errorf("QR pairing error: %w", evt.Error)
				}
				return nil, fmt.Errorf("QR pairing error")

			default:
				// Non-success terminal events are treated as failures.
				client.Disconnect()
				if evt.Error != nil {
					return nil, fmt.Errorf("pairing failed (%s): %w", evt.Event, evt.Error)
				}
				return nil, fmt.Errorf("pairing failed (%s)", evt.Event)
			}
		}

		client.Disconnect()
		return nil, fmt.Errorf("QR channel closed before pairing succeeded")
	}

	// Existing session: connect directly.
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect with existing session: %w", err)
	}

	return client, nil
}
