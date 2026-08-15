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


FROM debian:trixie-slim AS runtime

# libprotobuf/libnl are nsjail's runtime deps; the -dev packages guarantee the
# right sonames without guessing versioned runtime package names.
RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc g++ python3 openjdk-21-jdk-headless \
        libprotobuf-dev libnl-route-3-dev \
        procps iproute2 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=nsjail /src/nsjail/nsjail /usr/local/bin/nsjail
COPY docker/worker/spike.sh /opt/judge/spike.sh
RUN chmod +x /opt/judge/spike.sh

RUN useradd --uid 1000 --create-home --shell /usr/sbin/nologin judge

ENV JUDGE_SANDBOX_DRIVER=nsjail
WORKDIR /opt/judge
