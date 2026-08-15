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

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/judge ./cmd/judge


FROM debian:trixie-slim AS runtime

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
COPY docker/worker/spike.sh docker/worker/entrypoint.sh /opt/judge/
RUN chmod +x /opt/judge/spike.sh /opt/judge/entrypoint.sh

RUN useradd --uid 1000 --create-home --shell /usr/sbin/nologin judge \
    && mkdir -p /box /box/cache && chown -R judge:judge /box

WORKDIR /opt/judge
EXPOSE 2358
ENTRYPOINT ["/opt/judge/entrypoint.sh"]
CMD ["judge", "-config", "/opt/judge/configs/judge.conf"]
