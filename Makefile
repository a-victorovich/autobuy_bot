.PHONY: run build tidy lint

## run: run the scanner with default config
run:
	go run ./cmd/scanner -config config.yaml -log-level debug

## build: compile a binary to ./bin/scanner
build:
	go build -o bin/scanner ./cmd/scanner

## tidy: tidy and verify dependencies
tidy:
	go mod tidy
	go mod verify

## lint: run go vet
lint:
	go vet ./...
