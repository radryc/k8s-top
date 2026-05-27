# syntax=docker/dockerfile:1.7

FROM golang:1.26.3 AS build

ARG BUILD_VERSION=dev
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown

WORKDIR /src/k8s-top

COPY k8s-top/go.mod k8s-top/go.sum ./
RUN go mod download

COPY k8s-top/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
	-trimpath \
	-ldflags "-s -w -X github.com/rydzu/ainfra/k8s-top/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/rydzu/ainfra/k8s-top/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/rydzu/ainfra/k8s-top/internal/buildinfo.BuildTime=${BUILD_TIME}" \
	-o /out/k8s-top ./cmd/k8s-top

FROM gcr.io/distroless/base-debian12

COPY --from=build /out/k8s-top /k8s-top

ENTRYPOINT ["/k8s-top"]