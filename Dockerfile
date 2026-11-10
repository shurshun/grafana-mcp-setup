# Consumes the pre-built binary goreleaser drops into the build context.
# For a self-contained `docker build .` from source, see Dockerfile.local.
FROM gcr.io/distroless/static:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88

COPY grafana-mcp-setup /grafana-mcp-setup

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/grafana-mcp-setup"]
