# Consumes the pre-built binary goreleaser drops into the build context.
# For a self-contained `docker build .` from source, see Dockerfile.local.
FROM gcr.io/distroless/static:nonroot

COPY grafana-mcp-setup /grafana-mcp-setup

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/grafana-mcp-setup"]
