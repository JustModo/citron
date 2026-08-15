FROM debian:trixie-slim AS nsjail

ARG NSJAIL_VERSION=3.6

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates git make gcc g++ \
        autoconf bison flex libtool pkg-config \
        protobuf-compiler libprotobuf-dev libnl-route-3-dev \
    && rm -rf /var/lib/apt/lists/*

RUN git clone --depth=1 --branch $NSJAIL_VERSION https://github.com/google/nsjail /src/nsjail \
    && make -C /src/nsjail -j"$(nproc)" \
    && /src/nsjail/nsjail --help >/dev/null


FROM golang:1.26-trixie AS build

ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static and reproducible: no libc dependency to keep in step with the runtime image,
# and -trimpath so the binary carries no build-host paths.
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/judge ./cmd/judge \
    && /out/judge -version


FROM debian:trixie-slim AS runtime

ARG VERSION=dev
LABEL org.opencontainers.image.title="judge" \
      org.opencontainers.image.description="Sandboxed code execution service" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/JustModo/judge" \
      org.opencontainers.image.licenses="MIT"

# Toolchains, plus nsjail's runtime dependencies. The -dev packages are used to pull
# the right sonames without guessing versioned runtime package names.
RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc g++ python3 openjdk-21-jdk-headless \
        libprotobuf-dev libnl-route-3-dev \
        procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=nsjail /src/nsjail/nsjail /usr/local/bin/nsjail
COPY --from=build /out/judge /usr/local/bin/judge
COPY configs/ /opt/judge/configs/
COPY spike.sh entrypoint.sh /opt/judge/
RUN chmod +x /opt/judge/spike.sh /opt/judge/entrypoint.sh

RUN useradd --uid 1000 --create-home --shell /usr/sbin/nologin judge \
    && mkdir -p /box /box/cache && chown -R judge:judge /box

WORKDIR /opt/judge
EXPOSE 2358
ENTRYPOINT ["/opt/judge/entrypoint.sh"]
CMD ["judge", "-config", "/opt/judge/configs/judge.conf"]
