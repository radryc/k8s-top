# syntax=docker/dockerfile:1.7

FROM golang:1.26.4 AS build

ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /src/k8s-top

COPY go.mod go.sum ./
RUN go mod download

# v1.0.1 fixes otelc pinning the pre-rename module path (see monofs/Dockerfile).
ARG OTELC_VERSION=v1.0.1
RUN GOBIN=/usr/local/bin go install go.opentelemetry.io/otelc/tool/cmd/otelc@${OTELC_VERSION}

COPY . ./
# Keep -trimpath in GOFLAGS so otelc's nested instrumented builds use the
# same flag set as the top-level build; passing -trimpath only on the command
# line triggers a linker "fingerprint mismatch" for otelc/pkg/runtime.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-trimpath otelc go build \
	-ldflags "-s -w -X github.com/radryc/k8s-top/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/radryc/k8s-top/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/radryc/k8s-top/internal/buildinfo.BuildTime=${BUILD_TIME}" \
	-o /out/k8s-top ./cmd/k8s-top

FROM gcr.io/distroless/base-debian12

COPY --from=build /out/k8s-top /k8s-top

ENTRYPOINT ["/k8s-top"]