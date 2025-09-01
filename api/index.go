package main

import (
	"fmt"
	"net/http"

	"go.mau.fi/whatsmeow"
)

// Client is initialized in main.go
var Client *whatsmeow.Client

// homeHandler serves the home page
func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `
	<!DOCTYPE html>
	<html>
	<head>
		<title>WhatsApp HTTP API</title>
		<meta name="viewport" content="width=device-width, initial-scale=1.0">
		<style>
			body { font-family: Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; }
			h1 { color: #25D366; }
			.endpoint { 
				background: #f5f5f5; 
				padding: 15px; 
				border-radius: 5px; 
				margin: 10px 0; 
				font-family: monospace;
			}
		</style>
	</head>
	<body>
		<h1>WhatsApp HTTP API</h1>
		<p>Welcome to the WhatsApp HTTP API service. Here are the available endpoints:</p>
		
		<div class="endpoint">
			<strong>GET /</strong> - This homepage
		</div>
		<div class="endpoint">
			<strong>POST /send</strong> - Send a message (requires JSON body with 'to' and 'text')
		</div>
		<div class="endpoint">
			<strong>GET /health</strong> - Health check endpoint
		</div>
		<div class="endpoint">
			<strong>GET /logs</strong> - View application logs
		</div>
		<div class="endpoint">
			<strong>GET /qr</strong> - Get QR code for authentication
		</div>
	</body>
	</html>
	`)
}

// Handler handles all incoming HTTP requests
func Handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		homeHandler(w, r)
	case "/send":
		SendHandler(w, r)
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

// Client initialization is handled in main.go
