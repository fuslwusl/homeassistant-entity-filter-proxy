ARG BUILD_FROM

FROM golang:1.23-alpine AS builder
WORKDIR /src

COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/ha-ws-proxy .

FROM ${BUILD_FROM}

RUN apk add --no-cache jq

COPY --from=builder /out/ha-ws-proxy /usr/bin/ha-ws-proxy
COPY run.sh /run.sh

RUN chmod +x /usr/bin/ha-ws-proxy /run.sh

CMD ["/run.sh"]
