# ---- builder ----
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev linux-headers
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/gutter .

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget tesseract-ocr tesseract-ocr-data-eng
WORKDIR /app
COPY --from=builder /out/gutter /usr/local/bin/gutter
EXPOSE 8081
HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8081/healthz >/dev/null || exit 1
ENTRYPOINT ["gutter", "-config", "/data/config.yaml"]
