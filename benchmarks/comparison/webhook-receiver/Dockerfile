FROM golang:1.22-alpine AS build
WORKDIR /app
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go mod init webhook-receiver && CGO_ENABLED=0 GOOS=linux go build -o webhook-receiver .

FROM scratch
COPY --from=build /app/webhook-receiver /webhook-receiver
ENTRYPOINT ["/webhook-receiver"]
