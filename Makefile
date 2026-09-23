BIN   := bin/citron
IMAGE := citron:dev

.PHONY: build test test-race lint security image up down clean smoke stress

build:
	go build -o $(BIN) ./cmd/citron

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...
	gofmt -l -e .

security:
	go test -tags=security -count=1 -timeout=15m ./tests/

image:
	docker build -t $(IMAGE) .

up:
	docker compose up -d --build

down:
	docker compose down

smoke:
	node tests/smoke.mjs

stress:
	node bench/stress.js

clean:
	rm -rf bin
