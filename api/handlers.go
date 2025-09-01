package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
)

// qrCode stores the current QR code for authentication
var qrCode string
var qrCodeLock sync.Mutex

// logBuffer is a thread-safe buffer for storing logs
var logBuffer = struct {
	sync.RWMutex
	buf bytes.Buffer
}{}

// logsHandler handles the /logs endpoint
func logsHandler(w http.ResponseWriter, r *http.Request) {
	logBuffer.RLock()
	defer logBuffer.RUnlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write(logBuffer.buf.Bytes())
}

// healthHandler handles the /health endpoint
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("method not allowed"))
		return
	}

	response := map[string]string{
		"status":  "ok",
		"service": "whatsapp-http",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// SendHandler handles the /send endpoint
// qrHandler handles the /qr endpoint
func qrHandler(w http.ResponseWriter, r *http.Request) {
	qrCodeLock.Lock()
	defer qrCodeLock.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(qrCode))
}

// SetQRCode updates the QR code value in a thread-safe way
func SetQRCode(code string) {
	qrCodeLock.Lock()
	defer qrCodeLock.Unlock()
	qrCode = code
}

// GetQRCode retrieves the current QR code value in a thread-safe way
func GetQRCode() string {
	qrCodeLock.Lock()
	defer qrCodeLock.Unlock()
	return qrCode
}

func SendHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("method not allowed"))
		return
	}

	// Parse request body
	var req struct {
		To      string `json:"to"`
		Message string `json:"message"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid request body"))
		return
	}

	// TODO: Implement actual message sending logic
	// This is a placeholder response
	response := map[string]string{
		"status":  "success",
		"message": "Message queued for sending",
		"to":      req.To,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
