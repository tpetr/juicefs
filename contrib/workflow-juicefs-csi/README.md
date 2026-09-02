# Workflow JuiceFS CSI prototype

This is an isolated node-only CSI experiment for Semgrep workflow workspaces.
See [DESIGN.md](DESIGN.md) for the decision and safety boundary.

Phase 1 is intentionally limited to proving that a JuiceFS mount completed in
`NodePublishVolume` is usable by a `gvisor-netraw` pod. Do not interpret it as
the metadata handoff protocol.

## Local checks

```sh
cd contrib/workflow-juicefs-csi
make fmt test build
```

## Dev2 architecture proof

Build from the repository root, push a unique tag to the existing POC-capable
ECR repository, resolve its manifest-list digest, and replace the placeholder
in both YAML files with that digest before applying anything:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -f contrib/workflow-juicefs-csi/Dockerfile \
  -t 338683922796.dkr.ecr.us-west-2.amazonaws.com/semgrep/fuse-fd-csi-driver:workflow-juicefs-POC_TAG \
  --push .
aws ecr describe-images --repository-name semgrep/fuse-fd-csi-driver \
  --image-ids imageTag=workflow-juicefs-POC_TAG \
  --query 'imageDetails[0].imageDigest' --output text
kubectl config use-context dev2-semgrep-app-cluster
kubectl apply -f contrib/workflow-juicefs-csi/deploy/driver.yaml
kubectl apply -f contrib/workflow-juicefs-csi/deploy/architecture-proof.yaml
kubectl -n ai-workflows wait --for=condition=Ready pod/workflow-juicefs-architecture-proof --timeout=10m
kubectl -n ai-workflows logs workflow-juicefs-architecture-proof
```

Delete the proof pod and inspect node-driver logs to verify clean unmount. The
POC objects are deliberately named and scoped; do not use broad deletion:

```sh
kubectl -n ai-workflows delete pod workflow-juicefs-architecture-proof
kubectl -n ai-workflows logs daemonset/workflow-juicefs-csi -c driver --since=10m
```
