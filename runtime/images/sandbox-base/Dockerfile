# Fixed base image for all Sandbox containers (Runtime design §2.1).
# Build from the repository root so that execd/ is in the build context:
#   docker build --build-arg GOPROXY=https://goproxy.io,direct \
#     -f runtime/images/sandbox-base/Dockerfile -t sandbox-base:py3.13-uv0.13.7 .
#
# Pinned: Python 3.13 (base image tag), uv 0.13.7.

FROM golang:1.27-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY execd/go.mod execd/go.sum ./
RUN go mod download
COPY execd/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/execd ./cmd/execd

FROM python:3.13-slim-bookworm
RUN pip install --no-cache-dir uv==0.13.7 \
 && uv --version | grep -q '^uv 0.13.7 '
COPY --from=build /out/execd /usr/local/bin/execd
RUN mkdir -p /workspace /var/lib/execd /etc/execd
WORKDIR /workspace
# Runtime injects /etc/execd/config.json and sets EXECD_CONFIG at container creation.
ENV PATH=/workspace/.venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Execd must be PID 1: exec form, no shell wrapper.
ENTRYPOINT ["/usr/local/bin/execd"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/execd", "healthcheck"]
