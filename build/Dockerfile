# Data-only OCI image carrying the MDM assets tree at the image root.
# Obot mounts it directly as a Kubernetes image volume (see the
# mdmAssets values in obot's chart) and reads it via
# OBOT_SERVER_MDM_ASSETS_PATH — no entrypoint, no userland, nothing to
# execute or scan.
#
# Build from the repo root with dist/mdm-assets staged
# (`build/mdm-assets.sh <version>`):
#   docker build -f build/Dockerfile .
FROM scratch
COPY dist/mdm-assets /
