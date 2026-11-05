FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/gateway ./gateway \
    && CGO_ENABLED=0 go build -trimpath -o /out/auth ./services/auth \
    && CGO_ENABLED=0 go build -trimpath -o /out/project ./services/project \
    && CGO_ENABLED=0 go build -trimpath -o /out/issue ./services/issue \
    && CGO_ENABLED=0 go build -trimpath -o /out/activity ./services/activity \
    && CGO_ENABLED=0 go build -trimpath -o /out/ai ./services/ai \
    && CGO_ENABLED=0 go build -trimpath -o /out/mcp ./services/mcp
RUN CGO_ENABLED=0 go install -tags 'mysql' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3

FROM nats:2.10.26-alpine AS nats

FROM alpine:3.21
RUN apk add --no-cache bash ca-certificates curl tini netcat-openbsd \
    && addgroup -S app \
    && adduser -S -G app app \
    && mkdir -p /app/migrations /tmp/trackline-nats \
    && chown -R app:app /app /tmp/trackline-nats
COPY --from=build /out/ /usr/local/bin/
COPY --from=build /go/bin/migrate /usr/local/bin/migrate
COPY --from=nats /usr/local/bin/nats-server /usr/local/bin/nats-server
COPY db/migrations/ /app/migrations/
COPY deploy/render/start.sh /app/start.sh
RUN chmod 0555 /app/start.sh
USER app
ENV PATH="/usr/local/bin:${PATH}"
ENTRYPOINT ["tini", "--"]
CMD ["/app/start.sh"]
