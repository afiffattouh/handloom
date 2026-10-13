# syntax=docker/dockerfile:1
# One static binary in a small, non-root image that also has git, because the hub
# can hold a project's code (D106) and serves it with `git http-backend`. Nothing
# in this file runs a target-architecture program during the build: the Go
# binary is cross-compiled on the build machine, and the runtime files (git and
# a shell for the repository hook) are unpacked into a folder with apk's --root
# and --arch, so `docker build --platform linux/amd64` works from an arm64 box
# without emulation.
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

# git and the few files it needs, for the target architecture, unpacked on the build machine. Package
# scripts are skipped (they could not run for another architecture): busybox's links are made by hand
# (only /bin/sh is used, by git's hooks).
FROM --platform=$BUILDPLATFORM alpine:3.21 AS rootfs
ARG TARGETARCH
RUN set -eu; \
    case "${TARGETARCH:-amd64}" in amd64) arch=x86_64;; arm64) arch=aarch64;; *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1;; esac; \
    mkdir -p /rootfs/etc/apk /rootfs/tmp; \
    mkdir -p /rootfs/etc/apk/keys; cp /usr/share/apk/keys/"$arch"/* /rootfs/etc/apk/keys/; cp /etc/apk/repositories /rootfs/etc/apk/repositories; \
    apk add --root /rootfs --arch "$arch" --initdb --no-cache --no-scripts alpine-baselayout busybox ca-certificates git git-daemon; \
    ln -sf busybox /rootfs/bin/sh; chmod 1777 /rootfs/tmp; \
    echo 'handloom:x:65532:65532:handloom:/data:/sbin/nologin' >> /rootfs/etc/passwd; echo 'handloom:x:65532:' >> /rootfs/etc/group; \
    test -x /rootfs/usr/bin/git; ls /rootfs/usr/libexec/git-core/git-http-backend >/dev/null

FROM scratch
LABEL org.opencontainers.image.title="Handloom" \
      org.opencontainers.image.description="Run a team of coding agents on your own machines. A lead plans, workers build, your machine checks, you approve." \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/afiffattouh/handloom"
COPY --from=rootfs /rootfs/ /
COPY --from=build /out/handloom /handloom
COPY --from=build --chown=65532:65532 /out/data /data
ENV HANDLOOM_DATA=/data HANDLOOM_ADDR=0.0.0.0:7420 PATH=/usr/bin:/bin
VOLUME /data
EXPOSE 7420
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/handloom", "healthcheck"]
USER 65532:65532
ENTRYPOINT ["/handloom"]
CMD ["hub", "serve", "--auto-init"]
