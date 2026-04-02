# NFT Scanner

A Go service that monitors Getgems NFT listings and sends Telegram alerts when
items are priced below a configurable percentage of the collection floor price.

## How it works

1. On startup, fetches the **floor price** for every collection in `config.yaml`.
2. Every 30 seconds (configurable), calls the configured Getgems history
   endpoints and paginates forward using cursors.
3. For each NFT belonging to a watched collection, checks:
   ```
   salePrice < floorPrice × (1 - discountPercent / 100)
   ```
4. If the condition is met, sends a formatted **Telegram alert** to the
   configured chat.

## Prerequisites

- Go 1.22+
- A Telegram bot token ([create one via @BotFather](https://t.me/BotFather))
- The chat ID of the target channel/group

## Setup

```bash
# 1. Clone and enter the repo
git clone https://github.com/yourorg/nft-scanner
cd nft-scanner

# 2. Install dependencies
go mod tidy

# 3. Configure
cp config.yaml config.local.yaml
# Edit config.local.yaml — fill in bot_token, chat_id, and your collections

# 4. Run
go run ./cmd/scanner -config config.local.yaml
```

## Configuration

```yaml
telegram:
  bot_token: "7123456789:AAF..."  # From @BotFather
  chat_id: -1001234567890         # Negative for groups/channels

wallet:
  secret_phrase: ""               # Optional for now; 12 or 24 mnemonic words

scanner:
  poll_interval_seconds: 30       # How often to scan (default: 30)

getgems:
  api_key: "YOUR_GETGEMS_API_KEY_HERE"
  base_url: "https://api.getgems.io/public-api"  # Optional; defaults to this value
  web_url: "https://getgems.io"                  # Optional; used for alert links

collections:
  "EQD...address1": 10  # Alert if price < floorPrice * 0.90  (10% off)
  "EQD...address2": 15  # Alert if price < floorPrice * 0.85  (15% off)

gift_collections:
  "EQD...giftAddress1": 10  # Same threshold logic, but for /v1/nfts/history/gifts
```

## Running in production

```bash
# Build a binary
make build

# Run with environment-specific config
./bin/scanner -config ./config.local.yaml -log-level info
```

For a simple systemd service, create `/etc/systemd/system/nft-scanner.service`:

```ini
[Unit]
Description=NFT Scanner
After=network.target

[Service]
ExecStart=/usr/local/bin/scanner -config /etc/nft-scanner/config.yaml
Restart=on-failure
RestartSec=10s

[Install]
WantedBy=multi-user.target
```

## Project structure

```
.
├── cmd/
│   └── scanner/
│       └── main.go          # Entrypoint — wires deps and starts monitor
├── internal/
│   ├── config/
│   │   └── config.go        # YAML config loading & validation
│   ├── getgems/
│   │   └── client.go        # Getgems API HTTP client
│   ├── telegram/
│   │   └── notifier.go      # Telegram bot wrapper
│   ├── wallet/
│   │   └── wallet.go        # TON wallet mnemonic parsing and access
│   └── monitor/
│       └── monitor.go       # Core business logic
├── config.yaml              # Example configuration
├── go.mod
└── Makefile
```

## Run tests
go test ./internal/monitor
or
GOCACHE=/tmp/go-build-cache go test ./internal/monitor -run TestCalculateThreshold -v

## Plans to do:
1) Renew floor price for collections 
