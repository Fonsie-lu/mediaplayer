.PHONY: build run test clean fmt

BINARY=mediaplayer

build:
	go build -o $(BINARY) .

run:
	go run .

test:
	go test -v ./...

clean:
	rm -f $(BINARY)
	rm -rf /tmp/mediaplayer-*

fmt:
	gofmt -w .
