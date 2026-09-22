FROM alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates curl tzdata

COPY --chmod=755 komari-${TARGETOS}-${TARGETARCH} /app/komari

ENV GIN_MODE=release
ENV KOMARI_LISTEN=0.0.0.0:25774
ENV GODEBUG=disablethp=1

EXPOSE 25774

CMD ["/app/komari", "server"]
