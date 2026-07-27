ARG REGISTRY_HOST=127.0.0.1
FROM docker.io/library/golang:1.25.9-bookworm@sha256:c5f8e50bbd915fd0ca37963de056b79913ff78aa7f02a861fae21058dc9ba1e9 AS build

ARG TARGETOS=linux
ARG TARGETARCH=arm64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build \
        -trimpath \
        -ldflags="-s -w -X github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/app.version=container" \
        -o /out/dws \
        ./cmd \
    && CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build \
        -trimpath \
        -ldflags="-s -w -X github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/app.version=container" \
        -o /out/dwsd \
        ./cmd/dwsd

ARG REGISTRY_HOST=127.0.0.1
FROM docker.io/library/debian:bookworm-slim@sha256:9b67294679b30e5d6ab257b40594feeb4a4b81f7fcf4131f4decf0d6a212a9b0

ARG APP_UID=501
ARG APP_GID=501

ENV HOME=/home/dws \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TZ=Asia/Shanghai

RUN groupadd --gid "${APP_GID}" dws \
    && useradd \
        --uid "${APP_UID}" \
        --gid "${APP_GID}" \
        --home-dir "${HOME}" \
        --create-home \
        --shell /usr/sbin/nologin \
        dws \
    && install \
        --directory \
        --owner dws \
        --group dws \
        --mode 0700 \
        /var/lib/dws/config \
        /var/lib/dws/keychain \
    && apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build --chmod=0555 /out/dws /usr/local/bin/dws
COPY --from=build --chmod=0555 /out/dwsd /usr/local/bin/dwsd

USER dws

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/dwsd"]
