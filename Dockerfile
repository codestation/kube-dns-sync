FROM golang:1.22-alpine as builder

ARG CI_COMMIT_TAG
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum /src/
RUN go mod download
COPY . /src/

RUN set -ex; \
    CGO_ENABLED=0 go build -o release/kube-dns-sync \
    -trimpath \
    -ldflags "-w -s \
    -X main.Tag=${CI_COMMIT_TAG}"

FROM alpine:3.19
LABEL maintainer="codestation <codestation@megpoid.dev>"

RUN apk add --no-cache ca-certificates tzdata

RUN set -eux; \
    addgroup -S runner -g 1000; \
    adduser -S runner -G runner -u 1000

COPY --from=builder /src/release/kube-dns-sync /usr/local/bin/kube-dns-sync

USER runner

CMD ["/usr/local/bin/kube-dns-sync"]
