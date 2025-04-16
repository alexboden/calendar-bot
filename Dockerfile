# Use Go base image to build
FROM golang:1.24-bullseye AS builder

WORKDIR /app
COPY . .

RUN go mod tidy && go build -o calendar-bot .

# Use same OS for runtime to avoid glibc issues
FROM debian:bullseye-slim

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/calendar-bot .

COPY credentials.json token.json .env ./

ENTRYPOINT ["./calendar-bot"]

