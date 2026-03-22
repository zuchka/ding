# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /ding ./cmd/ding/

# Final stage — scratch for minimal image
FROM scratch
COPY --from=builder /ding /ding
EXPOSE 8080
ENTRYPOINT ["/ding", "serve", "--config", "/etc/ding/ding.yaml"]
