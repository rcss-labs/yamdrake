# Build stage: official Go image pinned to an exact patch release.
FROM golang:1.27.4 AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src

# Dependencies first so this layer is cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/rcss-labs/yumdrake/internal/version.Version=${VERSION} -X github.com/rcss-labs/yumdrake/internal/version.Commit=${COMMIT}" \
    -o /out/yumdrake-runner ./cmd/runner
# The artifact directory is created here because the final image has no shell.
RUN mkdir -p /out/artifacts

# Final stage: distroless static, nonroot (tag gcr.io/distroless/static-debian13:nonroot).
FROM gcr.io/distroless/static-debian13@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/yumdrake-runner /yumdrake-runner
# Owned by the service user so a named volume mounted here inherits the ownership.
COPY --from=build --chown=65532:65532 /out/artifacts /var/lib/yumdrake/artifacts
USER 65532:65532
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 CMD ["/yumdrake-runner","healthcheck"]
ENTRYPOINT ["/yumdrake-runner"]
