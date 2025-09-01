package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"strings"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
	qrterminal "github.com/mdp/qrterminal/v3"

	// SQLite driver without CGO
	_ "modernc.org/sqlite"
)

var (
	client *whatsmeow.Client
	ready  atomic.Bool
)

func reconnectAfterLogout() {
    for attempt := 1; attempt <= 5; attempt++ {
        if client == nil {
            return
        }
        // Ensure previous session is disconnected
        client.Disconnect()
        // Try reconnecting: this should trigger a new QR code event
        if err := client.Connect(); err != nil {
            backoff := time.Duration(attempt*2) * time.Second
            log.Printf("reconnect attempt %d failed: %v (retrying in %v)", attempt, err, backoff)
            time.Sleep(backoff)
            continue
        }
        log.Printf("Reconnected. If not logged in, a new QR will be shown for relinking.")
        return
    }
    log.Printf("failed to reconnect after logout; please restart the service if the issue persists")
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		log.Println("shutting down...")
		cancel()
		if client != nil {
			client.Disconnect()
		}

		os.Exit(0)
	}()

	// Initialize WhatsApp client (session persisted in SQLite)
	initClient(ctx)

	// HTTP server
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/health", healthHandler)
	addr := getEnv("ADDR", ":8080")
	log.Printf("HTTP server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server error: %v", err)
	}
}

func initClient(ctx context.Context) {
	dsn := getEnv("WHATSAPP_SQLITE_DSN", "file:whatsmeow.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	logger := waLog.Stdout("whatsmeow", "INFO", true)
	container, err := sqlstore.New(ctx, "sqlite", dsn, logger)
	if err != nil {
		log.Fatalf("failed to open sql store: %v", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		log.Fatalf("failed to get device: %v", err)
	}
	if device == nil {
		device = container.NewDevice()
	}

	client = whatsmeow.NewClient(device, nil)

	// Track connection state: wait for app state sync before allowing sends
	client.AddEventHandler(func(e interface{}) {
		switch evt := e.(type) {
		case *events.Connected:
			log.Printf("Connected as %s, waiting for app state sync...", client.Store.ID.User)
		case *events.AppStateSyncComplete:
			ready.Store(true)
			log.Printf("App state sync complete: %v. Ready to send.", evt.Name)
		case *events.Disconnected:
			ready.Store(false)
			_ = evt // not used, but keeps pattern similar
			log.Printf("Disconnected")
		case *events.LoggedOut:
			// Linked device was removed from the phone.
			// Clear readiness and trigger a reconnect to show a fresh QR.
			ready.Store(false)
			log.Printf("Logged out by primary device. Will reconnect to show a new QR for relinking...")
			go reconnectAfterLogout()
		}
	})

	qrCh, _ := client.GetQRChannel(ctx)
	go func() {
		for evt := range qrCh {
			switch evt.Event {
			case "code":
				log.Println("Scan this QR code with WhatsApp on your phone:")
				// Draw QR in terminal for easy scanning
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			case "success":
				log.Println("QR scan successful. Waiting for device to connect...")
			case "timeout":
				log.Println("QR code timeout, restarting login flow...")
			case "error":
				log.Printf("QR error: %v\n", evt.Error)
			}
		}
	}()

	if err := client.Connect(); err != nil {
		log.Fatalf("failed to connect: %v", err)
	}

	// Wait a short moment for state to settle on first login
	time.Sleep(1 * time.Second)
	if !client.IsLoggedIn() {
		log.Println("Client not logged in yet. Scan the QR code printed above.")
	}

	log.Println("WhatsApp client is ready.")
}

type sendRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

type sendResponse struct {
	ID string `json:"id"`
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("method not allowed"))
		return
	}
	if client == nil || !client.IsConnected() || !ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("whatsapp client not ready (connecting/syncing)"))
		return
	}
	if !client.IsLoggedIn() {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("whatsapp client not logged in (scan QR in server logs)"))
		return
	}

	var req sendRequest
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid json"))
		return
	}
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.Text) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("fields 'to' and 'text' are required"))
		return
	}

	jid := normalizeJID(req.To)
	msg := &waProto.Message{Conversation: proto.String(req.Text)}

	// Use independent context so a slow send isn't canceled when HTTP client disconnects
	// Also retry a few times if device/usync query is temporarily failing right after sync
	var resp whatsmeow.SendResponse
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		resp, err = client.SendMessage(ctx, jid, msg)
		cancel()
		if err == nil {
			break
		}
		es := err.Error()
		if strings.Contains(es, "failed to get device list") || strings.Contains(es, "usync") || strings.Contains(es, "info query") {
			backoff := time.Duration(attempt*2) * time.Second
			log.Printf("send attempt %d failed due to usync/device list, retrying in %v: %v", attempt, backoff, err)
			time.Sleep(backoff)
			continue
		}
		// Non-retriable error
		break
	}
	if err != nil {
		log.Printf("send message error: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to send message"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sendResponse{ID: resp.ID})
}

// healthHandler returns readiness and login state for external checks
func healthHandler(w http.ResponseWriter, r *http.Request) {
    type health struct {
        Connected bool `json:"connected"`
        LoggedIn  bool `json:"logged_in"`
        Ready     bool `json:"ready"`
    }
    h := health{
        Connected: client != nil && client.IsConnected(),
        LoggedIn:  client != nil && client.IsLoggedIn(),
        Ready:     ready.Load(),
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(h)
}

func normalizeJID(to string) types.JID {
	to = strings.TrimSpace(to)
	if strings.Contains(to, "@") {
		jid, err := types.ParseJID(to)
		if err == nil {
			return jid
		}
	}
	// Assume it's a phone number: strip non-digits (including '+')
	b := make([]rune, 0, len(to))
	for _, r := range to {
		if r >= '0' && r <= '9' {
			b = append(b, r)
		}
	}
	cleaned := string(b)
	return types.NewJID(cleaned, types.DefaultUserServer)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
