BIN := bin/judge
PKG := ./...

.PHONY: build test test-race lint integration security bench worker-image spike clean

build:
	go build -o $(BIN) ./cmd/judge

test:
	go test $(PKG)

test-race:
	go test -race $(PKG)

lint:
	go vet $(PKG)
	gofmt -l -e .

integration:
	go test -tags=integration -count=1 $(PKG)

security:
	go test -tags=security -count=1 ./tests/security/...

bench:
	go test -bench=. -benchmem -benchtime=20x ./benchmarks/...

worker-image:
	docker build -f docker/worker/Dockerfile -t judge-worker:dev .

spike: worker-image
	docker run --rm --cap-add=SYS_ADMIN --security-opt apparmor=unconfined \
		--cgroupns=private --network=none judge-worker:dev /opt/judge/spike.sh

clean:
	rm -rf bin dist
