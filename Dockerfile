
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache make build-base git

WORKDIR /app

COPY . .

RUN make build

FROM node:22-alpine AS frontend-builder

WORKDIR /app/frontend

COPY frontend/package.json ./
RUN npm install

COPY frontend ./
RUN npm run build

FROM alpine:latest

RUN apk add --no-cache fontconfig font-linux-libertine

WORKDIR /app

COPY --from=builder /app/bin /app/bin
COPY --from=frontend-builder /app/frontend/dist /app/frontend/dist

COPY static ./static
COPY templates ./templates

EXPOSE 8080

CMD ["/app/bin/miniwiki"]
