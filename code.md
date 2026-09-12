# Release contract

Publish the application and Helm chart together for every release.

For a release tag `vX.Y.Z`, all of these versions must be `X.Y.Z`:

- The application binary version and container image tag.
- `version` in `charts/grafana-mcp-setup/Chart.yaml`.
- `appVersion` in the same Chart.yaml.
- The published OCI chart version.

Update both Chart.yaml fields in the release preparation commit before creating
the tag. Package the chart with matching `--version` and `--app-version` values.
Leave the default `image.tag` empty so the Deployment uses `.Chart.AppVersion`.
Never publish a new application release while leaving the chart on an older
application version.

Verify that the published chart selects the image from the same release without
user overrides. Installing the newest chart must install the newest released
application. An explicitly selected older chart keeps its matching image version;
do not use a floating `latest` image tag.

Complete the release only after the binaries, multi-architecture image, and OCI
chart have been published and their versions and configured attestations checked.
