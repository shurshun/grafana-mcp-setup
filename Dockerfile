# Consumes the pre-built binary goreleaser drops into the build context.
# For a self-contained `docker build .` from source, see Dockerfile.local.
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

COPY grafana-mcp-setup /grafana-mcp-setup

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/grafana-mcp-setup"]
