FROM oven/bun:1@sha256:0733e50325078969732ebe3b15ce4c4be5082f18c4ac1a0f0ca4839c2e4e42a7 AS builder

WORKDIR /build/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY ./web ./
COPY ./VERSION /build/VERSION
RUN DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION=$(cat /build/VERSION) bun run build

FROM golang:1.26.1-alpine@sha256:2389ebfa5b7f43eeafbd6be0c3700cc46690ef842ad962f6c5bd6be49ed82039 AS builder2
ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off

ARG TARGETOS
ARG TARGETARCH
ENV GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}
ENV GOEXPERIMENT=greenteagc

WORKDIR /build

ADD go.mod go.sum ./
# relaykit is a local submodule referenced via replace; its go.mod must be
# present for go mod download to resolve the main module graph.
ADD relaykit/go.mod ./relaykit/go.mod
RUN go mod download

COPY . .
COPY --from=builder /build/web/dist ./web/dist
# Fork version stamps. .dockerignore excludes .git, so `git describe` cannot run
# inside the build; the build commit must be passed in:
#   docker build --build-arg QY_BUILD_VERSION="$(git describe --tags --always --dirty)" .
# Left empty, qianye/version reports "unknown" rather than a fabricated tag.
# The symbol path must be the FULL module path — the linker silently drops a
# -X whose path does not match, with no error and a successful build.
#
# The fork version and the synced-upstream commit are NOT injected here: both
# are declared in qianye/version/baseline.txt and compiled in via go:embed. The
# core version comes from that same declaration (upstream_tag, verbatim) via
# build.sh --print-core, which falls back to the VERSION file when upstream CI
# has written one. Both readers of the declaration parse it identically (exact
# key names, last occurrence wins).
#
# common.Version must stay byte-identical to the upstream release tag: the
# upstream "check for updates" button compares it to a release tag_name with
# string equality, so any fork suffix makes it report an update forever.
ARG QY_BUILD_VERSION=
RUN CORE_VERSION="$(sh qianye/scripts/build.sh --print-core)" \
    && go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=${CORE_VERSION}' -X 'github.com/QuantumNous/new-api/qianye/version.Build=${QY_BUILD_VERSION}'" -o new-api

FROM debian:bookworm-slim@sha256:f06537653ac770703bc45b4b113475bd402f451e85223f0f2837acbf89ab020a

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata libasan8 wget \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates

COPY --from=builder2 /build/new-api /
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/
EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/new-api"]
