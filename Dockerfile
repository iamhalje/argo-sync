FROM golang:1.25 AS builder

WORKDIR /src

ARG TARGETOS=linux
ARG TARGETARCH=amd64

ARG VERSION=unknown
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG REPO=github.com/iamhalje/argo-sync

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w \
        -X github.com/iamhalje/argo-sync/internal/buildinfo.Version=$VERSION \
        -X github.com/iamhalje/argo-sync/internal/buildinfo.Commit=$COMMIT \
        -X github.com/iamhalje/argo-sync/internal/buildinfo.BuildDate=$BUILD_DATE \
        -X github.com/iamhalje/argo-sync/internal/buildinfo.Repo=$REPO" \
      -o /out/argo-sync ./cmd/argo-sync

FROM alpine:3.23

COPY --from=builder /out/argo-sync /usr/local/bin/argo-sync

ENTRYPOINT ["/usr/local/bin/argo-sync"]
