FROM alpine:latest AS builder

RUN set -eux && apk add --no-cache iptables go

WORKDIR /app

COPY go.mod go.mod
RUN go mod download
COPY . .
RUN go build -o udp2faketcp

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/udp2faketcp /app/udp2faketcp
RUN chmod +x /app/udp2faketcp
ENTRYPOINT ["/app/udp2faketcp"]