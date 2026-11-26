BIN_DIR := bin

.PHONY: all build test vet clean

all: build

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -o $(BIN_DIR)/ ./cmd/...

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)
