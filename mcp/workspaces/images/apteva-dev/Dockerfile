FROM oven/bun:1-debian AS bun

FROM golang:1.25-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl git jq less procps python3 python3-pip ripgrep unzip \
    && rm -rf /var/lib/apt/lists/*

COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun

RUN groupadd --gid 1000 workspace \
    && useradd --uid 1000 --gid workspace --create-home --shell /bin/bash workspace \
    && mkdir -p /workspace /cache \
    && chown -R workspace:workspace /workspace /cache

ENV HOME=/workspace/.home \
    XDG_CACHE_HOME=/cache/xdg \
    GOCACHE=/cache/go/build \
    GOMODCACHE=/cache/go/pkg/mod \
    BUN_INSTALL_CACHE_DIR=/cache/bun \
    PIP_CACHE_DIR=/cache/pip

USER workspace
WORKDIR /workspace

CMD ["/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600; done"]
