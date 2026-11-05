FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp ./services/mcp

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
COPY --from=build /out/mcp /usr/local/bin/mcp
USER app
EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/mcp"]
