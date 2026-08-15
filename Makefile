BIN   := bin/judge
IMAGE := judge:dev

# Grants nsjail needs inside a container. Explained in docs/sandbox-spike.md; none of
# them is --privileged, and no Docker socket is mounted.
SANDBOX_FLAGS := --cap-add=SYS_ADMIN \
	--security-opt apparmor=unconfined \
	--security-opt systempaths=unconfined \
	--cgroupns=private

.PHONY: build test test-race lint integration security image spike up down clean

build:
	go build -o $(BIN) ./cmd/judge

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

# Needs a running judge. Start one with `make up`, or point JUDGE_URL elsewhere.
security:
	go test -tags=security -count=1 -timeout=15m ./tests/security/...

image:
	docker build -f docker/judge/Dockerfile -t $(IMAGE) .

# Verifies nsjail and cgroup v2 actually work on this host before trusting them.
spike: image
	docker run --rm $(SANDBOX_FLAGS) --network=none $(IMAGE) /opt/judge/spike.sh

up:
	docker compose up -d --build

down:
	docker compose down

clean:
	rm -rf bin dist
