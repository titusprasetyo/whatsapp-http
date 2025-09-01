# whatsapp-http

Minimal HTTP wrapper around WhatsApp via whatsmeow. Provides a simple `/send` endpoint and `/health` for readiness.

## Features
- Send text messages via HTTP
- Health/readiness endpoint
- Persistent session using SQLite (file)
- Auto-reconnect after logout and re-emit QR in logs

## Requirements
- Go 1.21+ (for local build) or Docker

## Quick start (local)
```bash
# Install deps and run
go mod download
go run .
```
Check logs for a QR code on first run. Scan it with WhatsApp on your phone (Linked devices).

## Docker
Build and run with a persistent data volume so the session survives restarts:
```bash
docker build -t whatsapp-http:latest .

# create a local data dir for the sqlite db
mkdir -p ./data

docker run -d --name whatsapp-http \
  -p 8080:8080 \
  -v $(pwd)/data:/data \
  -e ADDR=":8080" \
  -e WHATSAPP_SQLITE_DSN="file:/data/whatsmeow.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)" \
  whatsapp-http:latest
```

## Endpoints
- POST `/send`
  - Request JSON:
    ```json
    { "to": "+6281234567890", "text": "Hello" }
    ```
  - Notes:
    - `to` may be a phone number or a full JID like `123@s.whatsapp.net`.

- GET `/health`
  - Response JSON:
    ```json
    { "connected": true, "logged_in": true, "ready": true }
    ```

## Environment
- `ADDR` (default `:8080`) — HTTP listen address
- `WHATSAPP_SQLITE_DSN` — SQLite DSN for whatsmeow session store. Example:
  ```
  file:/data/whatsmeow.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)
  ```

## Behavior notes
- On first run or after logout, the server prints a QR in the logs; scan it to link.
- If the phone unlinks this device, the app attempts to reconnect and emit a new QR.

## Examples
Send a message:
```bash
curl -X POST http://localhost:8080/send \
  -H 'Content-Type: application/json' \
  -d '{"to":"+6281234567890","text":"Hello from API"}'
```
Health:
```bash
curl -s http://localhost:8080/health | jq
```
