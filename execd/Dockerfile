# Verification image for Execd (plan P10). The production base image is owned by Runtime.
# Build:  docker build -t execd:dev .

FROM golang:1.27-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/execd ./cmd/execd

FROM debian:bookworm-slim
COPY --from=build /out/execd /usr/local/bin/execd
RUN mkdir -p /workspace /var/lib/execd
WORKDIR /workspace
# PATH is configured here by Runtime's convention; Execd inherits it.
ENV PATH=/workspace/.venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Execd must be PID 1 (exec form, no shell wrapper).
ENTRYPOINT ["/usr/local/bin/execd"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/execd", "healthcheck"]
