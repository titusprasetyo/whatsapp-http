package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	qrterminal "github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	// SQLite driver without CGO
	_ "modernc.org/sqlite"
)

// logBufferType implements io.Writer for capturing logs
// and makes it safe for concurrent access
type logBufferType struct {
	sync.RWMutex
	buf bytes.Buffer
}

func (l *logBufferType) Write(p []byte) (n int, err error) {
	l.Lock()
	defer l.Unlock()
	return l.buf.Write(p)
}

var (
	client *whatsmeow.Client
	ready  atomic.Bool

	// rateLimiter tracks the last message time per recipient
	rateLimiter = struct {
		sync.RWMutex
		lastMessage map[string]time.Time
	}{lastMessage: make(map[string]time.Time)}

	// For web UI
	logBuffer  = &logBufferType{}
	qrCode     string
	qrCodeLock sync.Mutex
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

	// Initialize log writer to capture logs for web UI
	log.SetOutput(io.MultiWriter(os.Stdout, logBuffer))

	// HTTP server
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/logs", logsHandler)
	http.HandleFunc("/qr", qrHandler)
	addr := getEnv("ADDR", ":8080")
	log.Printf("HTTP server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server error: %v", err)
	}
}

const htmlTemplate = `
<!DOCTYPE html>
<html>
<head>
    <title>WhatsApp HTTP</title>
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body { font-family: Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; }
        .container { display: flex; flex-direction: column; gap: 20px; }
        .qr-container { 
            text-align: center; 
            margin: 20px auto;
            max-width: 300px;
            padding: 15px;
            background: white;
            border: 1px solid #ddd;
            border-radius: 8px;
        }
        .logs { 
            background: #f5f5f5; 
            border: 1px solid #ddd; 
            border-radius: 4px; 
            padding: 10px; 
            height: 300px; 
            overflow-y: auto;
            font-family: monospace;
            white-space: pre-wrap;
        }
        .refresh-btn { 
            padding: 8px 16px; 
            background: #4CAF50; 
            color: white; 
            border: none; 
            border-radius: 4px; 
            cursor: pointer;
        }
        .refresh-btn:hover { background: #45a049; }
    </style>
</head>
<body>
    <div class="container">
        <h1>WhatsApp HTTP Interface</h1>
        
        <div class="qr-container">
            <h2>QR Code</h2>
            <div id="qr">{{.QRCode}}</div>
            <p>Scan this QR code with WhatsApp on your phone</p>
            <button class="refresh-btn" onclick="location.reload()">Refresh</button>
        </div>

        <div>
            <h2>Status: <span id="status" style="color: {{if .IsConnected}}green{{else}}red{{end}};" id="status-text">
                {{if .IsConnected}}Connected{{else}}Disconnected{{end}}
            </span></h2>
            <p>Logged in as: {{.PhoneNumber}}</p>
        </div>

        <div>
            <h2>Logs</h2>
            <div class="logs" id="logs">{{.Logs}}</div>
        </div>
    </div>
    <script>
        // Auto-scroll logs to bottom and auto-refresh
        function updateLogs() {
            fetch('/logs')
                .then(response => response.text())
                .then(logs => {
                    const logsDiv = document.getElementById('logs');
                    logsDiv.textContent = logs;
                    logsDiv.scrollTop = logsDiv.scrollHeight;
                });
        }

        // Update logs every 2 seconds
        updateLogs();
        setInterval(updateLogs, 2000);

        // Check connection status every 5 seconds
        function checkConnection() {
            fetch('/health')
                .then(response => response.json())
                .then(data => {
                    const statusEl = document.getElementById('status');
                    const statusText = document.getElementById('status-text');
                    if (data.connected && data.logged_in) {
                        statusEl.style.color = 'green';
                        statusText.textContent = 'Connected';
                    } else {
                        statusEl.style.color = 'red';
                        statusText.textContent = 'Disconnected';
                        // Auto-refresh QR code if disconnected
                        if (document.getElementById('qr').textContent.trim() === '') {
                            location.reload();
                        }
                    }
                });
        }
        checkConnection();
        setInterval(checkConnection, 5000);
    </script>
</body>
</html>
`

type homeTemplateData struct {
	QRCode      template.HTML
	IsConnected bool
	PhoneNumber string
	Logs        string
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Get QR code if available
	qrCodeLock.Lock()
	qr := qrCode
	qrCodeLock.Unlock()

	// Get logs
	logBuffer.RLock()
	logs := logBuffer.buf.String()
	logBuffer.RUnlock()

	// Get connection status
	isConnected := client != nil && client.IsConnected()
	phoneNumber := ""
	if client != nil && client.Store != nil && client.Store.ID != nil {
		phoneNumber = client.Store.ID.User
	}

	tmpl, err := template.New("home").Parse(htmlTemplate)
	if err != nil {
		http.Error(w, "Error loading template", http.StatusInternalServerError)
		return
	}

	data := homeTemplateData{
		QRCode:      template.HTML(qr),
		IsConnected: isConnected,
		PhoneNumber: phoneNumber,
		Logs:        logs,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("Error executing template: %v", err)
	}
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	logBuffer.RLock()
	defer logBuffer.RUnlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write(logBuffer.buf.Bytes())
}

func qrHandler(w http.ResponseWriter, r *http.Request) {
	qrCodeLock.Lock()
	defer qrCodeLock.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(qrCode))
}

func init() {
	// Initialize log buffer with current time
	logBuffer.Lock()
	logBuffer.buf.WriteString(fmt.Sprintf("[%s] WhatsApp HTTP service starting...\n", time.Now().Format("2006-01-02 15:04:05")))
	logBuffer.Unlock()
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

				// Generate QR code for terminal
				log.Println("Scan this QR code with WhatsApp on your phone:")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)

				// Generate a clickable link as fallback
				qrCodeLock.Lock()
				qrCode = fmt.Sprintf(`<div style="margin: 20px 0; padding: 15px; background: #f8f9fa; border-radius: 8px;">
                <p>Scan this QR code with WhatsApp:</p>
                <div style="background: white; padding: 15px; display: inline-block; border: 1px solid #ddd; border-radius: 4px;">
                    <pre style="margin: 0; line-height: 1.2;">%s</pre>
                </div>
                <p style="margin-top: 10px;">Or <a href="https://wa.me/%s?text=%s" target="_blank" style="color: #25D366; text-decoration: none; font-weight: bold;">click here</a> to open WhatsApp.</p>
            </div>`,
							template.HTMLEscapeString(generateQRCode(evt.Code)),
					evt.Code,
					"I'm trying to link my device")
				qrCodeLock.Unlock()

				log.Println("QR code generated and displayed in web interface")
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

	// Rate limiting check (10 seconds per recipient)
	rateLimiter.RLock()
	lastMsgTime, exists := rateLimiter.lastMessage[req.To]
	rateLimiter.RUnlock()

	if exists {
		timeSinceLast := time.Since(lastMsgTime)
		if timeSinceLast < 10*time.Second {
			waitTime := 10*time.Second - timeSinceLast
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limit exceeded: please wait " + waitTime.Round(time.Second).String() + " before sending another message to this recipient"))
			return
		}
	}

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

	// Update rate limiter on successful send
	rateLimiter.Lock()
	rateLimiter.lastMessage[req.To] = time.Now()
	rateLimiter.Unlock()

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
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return def
}

// generateQRCode generates a QR code as a string
func generateQRCode(code string) string {
	var buf bytes.Buffer
	config := qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    &buf,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	}
	qrterminal.GenerateWithConfig(code, config)
	return buf.String()
}
