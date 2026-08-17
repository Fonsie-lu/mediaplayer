.PHONY: build run test race lint vet fmt fmt-check check e2e clean

BINARY=mediaplayer

build:
	go build -o $(BINARY) .

run:
	go run .

test:
	go test -v ./...

# The session/batch code is concurrent enough that the race detector earns its
# runtime; CI runs this rather than plain `test`.
race:
	go test -race ./...

vet:
	go vet ./...

# staticcheck is pinned as a tool dependency in go.mod, so this needs no
# separate install and every machine runs the same version.
lint:
	go tool staticcheck ./...

fmt:
	gofmt -w .

# Same check CI makes: report files gofmt would change, and fail if any.
fmt-check:
	@out="$$(gofmt -l . | grep -v '^\.claude/' || true)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Browser-driven checks against a real Chromium — the only way to verify the
# page's keyboard handling (Chromium's media controls swallow keydown from a
# closed shadow root) and that the browser page's ES module graph actually
# resolves at runtime. Needs chromium, node and ffmpeg, so it is deliberately
# NOT part of `check`, which must run anywhere. Override CHROME= if yours lives
# elsewhere.
e2e: build
	@cd test/e2e && [ -d node_modules ] || npm install --no-audit --no-fund
	node test/e2e/harness.mjs

# Everything the CI gate runs, in the order that fails fastest.
check: fmt-check vet lint race

clean:
	rm -f $(BINARY)
	rm -rf /tmp/mediaplayer-*
