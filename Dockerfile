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
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/citron ./cmd/citron \
    && /out/citron -version


FROM debian:trixie-slim AS runtime

ARG VERSION=dev
LABEL org.opencontainers.image.title="citron" \
      org.opencontainers.image.description="Sandboxed code execution service" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/JustModo/citron" \
      org.opencontainers.image.licenses="MIT"

RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc g++ python3 openjdk-21-jdk-headless \
        libprotobuf-dev libnl-route-3-dev \
        procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=nsjail /src/nsjail/nsjail /usr/local/bin/nsjail
COPY --from=build /out/citron /usr/local/bin/citron
COPY configs/ /opt/citron/configs/
COPY spike.sh entrypoint.sh /opt/citron/
RUN chmod +x /opt/citron/spike.sh /opt/citron/entrypoint.sh

RUN useradd --uid 1000 --create-home --shell /usr/sbin/nologin citron \
    && mkdir -p /box /box/cache && chown -R citron:citron /box

WORKDIR /opt/citron
EXPOSE 2358
ENTRYPOINT ["/opt/citron/entrypoint.sh"]
CMD ["citron", "-config", "/opt/citron/configs/citron.conf"]
