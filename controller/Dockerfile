FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
RUN npm run build

FROM golang:1.26-alpine AS builder
WORKDIR /src/controller
COPY controller/go.mod controller/go.sum ./
RUN go mod download
COPY controller/ .
COPY --from=web-builder /web/dist ./internal/web/static
ARG PROJECT_VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
ARG MOSDNS_BASE=v5.3.4
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/managed-dns/controller/internal/version.ProjectVersion=${PROJECT_VERSION} -X github.com/managed-dns/controller/internal/version.GitCommit=${GIT_COMMIT} -X github.com/managed-dns/controller/internal/version.MosdnsBase=${MOSDNS_BASE} -X github.com/managed-dns/controller/internal/version.BuildTime=${BUILD_TIME}" -o /out/controller ./cmd/controller

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S controller && adduser -S -G controller -u 10001 controller
COPY --from=builder /out/controller /usr/local/bin/controller
USER controller
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/controller"]
