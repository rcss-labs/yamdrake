# The demo helper (cmd/yumdrake-demo): a fake GitHub with sample data, and the
# seeding of a fresh Yumdrake. A development tool: it is built only by
# deploy/compose/compose.demo.yaml and is never part of the product images.

# Build stage: official Go image pinned to an exact patch release.
FROM golang:1.27.4 AS build
WORKDIR /src

# Dependencies first so this layer is cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/yumdrake-demo ./cmd/yumdrake-demo

# Final stage: distroless static, nonroot (tag gcr.io/distroless/static-debian13:nonroot).
FROM gcr.io/distroless/static-debian13@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/yumdrake-demo /yumdrake-demo
COPY deploy/compose/demo/ /demo/
USER 65532:65532
HEALTHCHECK --interval=5s --timeout=3s --start-period=20s --retries=30 CMD ["/yumdrake-demo","healthcheck"]
ENTRYPOINT ["/yumdrake-demo"]
