FROM golang:1.24-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/pharos-watchtower ./cmd/monitor

FROM alpine:3.20

RUN apk add --no-cache ca-certificates

ENV PWT_HOME=/root/.pwt

WORKDIR /app

COPY --from=builder /out/pharos-watchtower /app/pharos-watchtower

EXPOSE 8080 8089

ENTRYPOINT ["/app/pharos-watchtower", "start"]
