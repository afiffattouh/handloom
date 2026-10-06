# syntax=docker/dockerfile:1
# One static binary in a distroless, non-root image. The builder runs on the
# build machine and cross-compiles, so `docker build --platform linux/amd64`
# works from an arm64 box without emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X handloom/internal/cli.Version=$VERSION" -o /out/handloom ./cmd/handloom
# /data is created here, owned by the runtime user, so a fresh named volume inherits it.
RUN mkdir -p /out/data && chown 65532:65532 /out/data && chmod 700 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/handloom /handloom
COPY --from=build --chown=65532:65532 /out/data /data
ENV HANDLOOM_DATA=/data HANDLOOM_ADDR=0.0.0.0:7420
VOLUME /data
EXPOSE 7420
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/handloom", "healthcheck"]
USER nonroot
ENTRYPOINT ["/handloom"]
CMD ["hub", "serve", "--auto-init"]
