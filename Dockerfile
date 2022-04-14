FROM golang:1.18-alpine3.15 as builder
COPY . /go/src/github.com/visaratechnology/docker-volume-davfs
WORKDIR /go/src/github.com/visaratechnology/docker-volume-davfs

RUN set -ex && apk add --no-cache --virtual .build-deps git gcc libc-dev && go install --ldflags '-extldflags "-static"' && apk del .build-deps 
CMD ["/go/bin/docker-volume-davfs"]

FROM alpine:3.15
RUN apk add --no-cache davfs2
RUN mkdir -p /run/docker/plugins /mnt/state /mnt/volumes
COPY davfs2.conf /etc/davfs2/davfs2.conf
COPY --from=builder /go/bin/docker-volume-davfs .
CMD ["docker-volume-davfs"]
