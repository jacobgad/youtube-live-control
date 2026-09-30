FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${BUILD_VERSION}" -o /out/youtube-live-control .

FROM alpine:3.24

ARG BUILD_VERSION=dev
ARG BUILD_ARCH

RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/youtube-live-control /usr/local/bin/youtube-live-control

LABEL \
  io.hass.name="YouTube Live Control" \
  io.hass.description="Schedule, start and end YouTube live streams from Home Assistant" \
  io.hass.version="${BUILD_VERSION}" \
  io.hass.type="app" \
  io.hass.arch="aarch64|amd64"

CMD ["/usr/local/bin/youtube-live-control"]
