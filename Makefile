.PHONY: run build tidy lint generate-getgems-openapi generate-toncenter-openapi generate-gifts-config

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

## generate-getgems-openapi: generate Getgems OpenAPI client/models
generate-getgems-openapi:
	go run ./cmd/getgems-openapi-gen

## generate-toncenter-openapi: generate TON Center OpenAPI client/models
generate-toncenter-openapi:
	go run ./cmd/toncenter-openapi-gen

## generate-gifts-config: print gift collections as YAML block (pass extra flags via ARGS)
generate-gifts-config:
	go run ./cmd/generate-gifts-config $(ARGS)
