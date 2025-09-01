// +build vercel

package main

import (
	"context"
	"net/http"
	"os"

	"go.mau.fi/whatsmeow"
)

var client *whatsmeow.Client

// Handler handles all incoming HTTP requests
func Handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		homeHandler(w, r)
	case "/send":
		sendHandler(w, r)
	case "/health":
		healthHandler(w, r)
	case "/logs":
		logsHandler(w, r)
	case "/qr":
		qrHandler(w, r)
	default:
		http.NotFound(w, r)
	}
}

// This init function will run when the serverless function starts
func init() {
	// Initialize WhatsApp client
	ctx := context.Background()
	initClient(ctx)
}
