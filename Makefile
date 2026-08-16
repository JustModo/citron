BIN   := bin/citron
IMAGE := citron:dev

SANDBOX_FLAGS := --cap-add=SYS_ADMIN \
	--security-opt apparmor=unconfined \
	--security-opt systempaths=unconfined \
	--cgroupns=private

.PHONY: build test test-race lint integration security image spike up down clean

build:
	go build -o $(BIN) ./cmd/citron

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...
	gofmt -l -e .

integration:
	go test -tags=integration -count=1 ./...

security:
	go test -tags=security -count=1 -timeout=15m ./tests/security/...

image:
	docker build -t $(IMAGE) .

spike: image
	docker run --rm $(SANDBOX_FLAGS) --network=none $(IMAGE) /opt/citron/spike.sh

up:
	docker compose up -d --build

down:
	docker compose down

clean:
	rm -rf bin dist
