# Copyright 2026 Semgrep, Inc.
# Licensed under the Apache License, Version 2.0.

FROM golang:1.25.10-bookworm AS juicefs-builder
WORKDIR /src
COPY . .
RUN make juicefs

FROM golang:1.25.10-bookworm AS driver-builder
WORKDIR /src
COPY contrib/workflow-juicefs-csi/go.mod contrib/workflow-juicefs-csi/go.sum ./
RUN go mod download
COPY contrib/workflow-juicefs-csi/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workflow-juicefs-csi-driver ./cmd/workflow-juicefs-csi-driver
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workspace-csi-control ./cmd/workspace-csi-control

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates fuse3 mount \
    && rm -rf /var/lib/apt/lists/*
COPY --from=juicefs-builder /src/juicefs /usr/local/bin/juicefs
COPY --from=driver-builder /out/workflow-juicefs-csi-driver /usr/local/bin/workflow-juicefs-csi-driver
COPY --from=driver-builder /out/workspace-csi-control /usr/local/bin/workspace-csi-control
ENTRYPOINT ["/usr/local/bin/workflow-juicefs-csi-driver"]
