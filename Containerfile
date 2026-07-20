# syntax=docker/dockerfile:1.7

FROM golang:1.26.1-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG GIT_HASH=unknown
ARG BUILD_TIME=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.gitHash=${GIT_HASH} -X main.buildTime=${BUILD_TIME}" \
      -o /out/koios .

RUN mkdir -p \
    /out/workspace/db \
    /out/workspace/sessions \
    /out/workspace/cron \
    /out/workspace/agents \
    /out/workspace/peers \
    /out/workspace/workflows \
    /out/workspace/runs \
    /out/workspace/browser \
    /out/workspace/extensions

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/koios /usr/local/bin/koios
COPY --from=build --chown=nonroot:nonroot /out/workspace /app/workspace

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/koios"]
CMD ["serve"]
