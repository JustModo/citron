BIN   := bin/citron
IMAGE := citron:dev

# Grants nsjail needs inside a container. Explained in docs/sandbox.md; none of
# them is --privileged, and no Docker socket is mounted.
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

# Needs real toolchains on the host.
integration:
	go test -tags=integration -count=1 ./...

# Needs a running citron. Start one with `make up`, or point CITRON_URL elsewhere.
security:
	go test -tags=security -count=1 -timeout=15m ./tests/security/...

image:
	docker build -t $(IMAGE) .

# Verifies nsjail and cgroup v2 actually work on this host before trusting them.
spike: image
	docker run --rm $(SANDBOX_FLAGS) --network=none $(IMAGE) /opt/citron/spike.sh

up:
	docker compose up -d --build

down:
	docker compose down

clean:
	rm -rf bin dist
