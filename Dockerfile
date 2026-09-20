FROM alpine:3.20
ARG TARGETPLATFORM

RUN apk add --no-cache ca-certificates tzdata

COPY $TARGETPLATFORM/rss-server /usr/local/bin/rss-server

ENV RSS_ADDR=:8080 \
    RSS_DB=/data/rss.db

VOLUME /data

EXPOSE 8080

ENTRYPOINT ["rss-server"]