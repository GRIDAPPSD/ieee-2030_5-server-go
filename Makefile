.PHONY: build test test-cover lint vet clean run

BINARY := bin/sep2server
MODULE := github.com/craig8/ieee-2030_5-go

build:
	go build -o $(BINARY) ./cmd/sep2server/

test:
	go test ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

test-verbose:
	go test -v ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

clean:
	rm -rf bin/ coverage.out coverage.html

run: build
	./$(BINARY) serve
