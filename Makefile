VERSION ?= dev

.PHONY: all ui build test run docker clean

all: build

ui:
	cd web && npm ci && npm run build

build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/bragi ./cmd/bragi

test:
	go vet ./...
	go test ./...

# Runs with a local ./config folder on http://localhost:8787
run: build
	BRAGI_CONFIG_DIR=./config ./bin/bragi

docker:
	docker build --build-arg VERSION=$(VERSION) -t bragi:$(VERSION) .

clean:
	rm -rf bin web/dist/assets web/dist/index.html web/dist/icon.svg
