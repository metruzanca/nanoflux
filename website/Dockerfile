FROM docker.io/alpine:3.20 AS build
RUN apk add --no-cache curl tar
ARG HUGO_VERSION=0.166.0
RUN curl -fsSL "https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_${HUGO_VERSION}_linux-amd64.tar.gz" -o /tmp/hugo.tar.gz \
    && tar -xzf /tmp/hugo.tar.gz -C /usr/local/bin hugo \
    && hugo version
WORKDIR /site
COPY . /site
RUN hugo --gc --minify

FROM docker.io/caddy:2-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/public /srv
EXPOSE 8080