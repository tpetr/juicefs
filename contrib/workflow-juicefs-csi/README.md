# Workflow JuiceFS CSI prototype

This is an isolated node-only CSI experiment for Semgrep workflow workspaces.
See [DESIGN.md](DESIGN.md) for the decision and safety boundary.
See [RESULTS.md](RESULTS.md) for the recorded dev2 evidence.

The prototype first proved that a JuiceFS mount completed in
`NodePublishVolume` is usable by a `gvisor-netraw` pod, then added immutable
metadata generations, an explicit success marker, and a Kubernetes Lease.
It is a POC with deliberately incomplete restart recovery, not a production
storage driver.

## Local checks

```sh
cd contrib/workflow-juicefs-csi
make fmt test build
```

## Build and deploy to dev2

Build from the repository root, push a unique tag to the existing POC-capable
ECR repository, resolve its manifest-list digest, and pin that digest in every
manifest before applying anything:

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
kubectl -n ai-workflows rollout status daemonset/workflow-juicefs-csi --timeout=10m
```

The checked-in manifests currently use the tested multi-architecture image
index `sha256:f4efcfec770ca4f2756111212f891d3ff24a368fc51800cb5cbb6c1c6f847d1a`.

## Dev2 tests

Run the single-pod architecture proof, inspect its output, then delete the pod
to trigger the marked-success commit:

```sh
kubectl apply -f contrib/workflow-juicefs-csi/deploy/architecture-proof.yaml
kubectl -n ai-workflows wait --for=condition=Ready pod/workflow-juicefs-architecture-proof --timeout=10m
kubectl -n ai-workflows logs workflow-juicefs-architecture-proof
kubectl -n ai-workflows delete pod workflow-juicefs-architecture-proof
```

The linear tests use scheduling gates so teardown and pointer publication are
complete before the replacement starts. After each first pod exits, capture
its logs, delete that exact pod if kubelet has not yet unpublished its inline
volume, wait for the matching Lease to disappear, and remove the replacement's
gate:

```sh
kubectl apply -f contrib/workflow-juicefs-csi/deploy/linear-handoff.yaml
kubectl -n ai-workflows wait --for=jsonpath='{.status.phase}'=Succeeded pod/workflow-juicefs-handoff-a --timeout=10m
kubectl -n ai-workflows logs workflow-juicefs-handoff-a
kubectl -n ai-workflows delete pod workflow-juicefs-handoff-a --ignore-not-found
kubectl -n ai-workflows wait --for=delete lease/workspace-3f196c85c34d4726fca3a9e7 --timeout=10m
kubectl -n ai-workflows patch pod workflow-juicefs-handoff-b --type=json \
  -p='[{"op":"remove","path":"/spec/schedulingGates"}]'
kubectl -n ai-workflows wait --for=jsonpath='{.status.phase}'=Succeeded pod/workflow-juicefs-handoff-b --timeout=10m
kubectl -n ai-workflows logs workflow-juicefs-handoff-b

kubectl apply -f contrib/workflow-juicefs-csi/deploy/failed-attempt.yaml
kubectl -n ai-workflows wait --for=jsonpath='{.status.phase}'=Succeeded pod/workflow-juicefs-failed-attempt --timeout=10m
kubectl -n ai-workflows delete pod workflow-juicefs-failed-attempt --ignore-not-found
kubectl -n ai-workflows wait --for=delete lease/workspace-3f196c85c34d4726fca3a9e7 --timeout=10m
kubectl -n ai-workflows patch pod workflow-juicefs-failed-replacement --type=json \
  -p='[{"op":"remove","path":"/spec/schedulingGates"}]'
kubectl -n ai-workflows wait --for=jsonpath='{.status.phase}'=Succeeded pod/workflow-juicefs-failed-replacement --timeout=10m
kubectl -n ai-workflows logs workflow-juicefs-failed-replacement
```

For contention, apply `deploy/contention.yaml`, wait until the owner is running,
and inspect the challenger's events for a retryable `Aborted` mount failure.
Delete the two exact pod names afterward. Use driver logs to inspect lifecycle
stages; the driver never needs an S3 list operation:

```sh
kubectl apply -f contrib/workflow-juicefs-csi/deploy/contention.yaml
kubectl -n ai-workflows wait --for=condition=Ready pod/workflow-juicefs-contention-owner --timeout=10m
kubectl -n ai-workflows describe pod workflow-juicefs-contention-challenger
kubectl -n ai-workflows delete pod workflow-juicefs-contention-owner workflow-juicefs-contention-challenger --ignore-not-found
kubectl -n ai-workflows logs daemonset/workflow-juicefs-csi -c driver --since=30m
```

The named test objects and immutable S3 generations are intentionally retained
unless an operator deletes those exact keys. Do not use broad prefix deletion.
