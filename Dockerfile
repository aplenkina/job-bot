FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jobbot ./cmd/jobbot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir /data && chown app /data
USER app
WORKDIR /app
COPY --from=build /out/jobbot /app/jobbot
VOLUME /data
ENTRYPOINT ["/app/jobbot", "-config", "/app/config.yaml"]
