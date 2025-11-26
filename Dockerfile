
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache make build-base git

WORKDIR /app

COPY . .

RUN make build

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bin /app/bin

COPY static ./static
COPY templates ./templates

EXPOSE 8080

CMD ["/app/bin/miniwiki"]
