FROM klakegg/hugo:0.166.0-ext-alpine AS build
WORKDIR /site
COPY . /site
RUN hugo --gc --minify

FROM caddy:2-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY --from=build /site/public /srv
EXPOSE 8080