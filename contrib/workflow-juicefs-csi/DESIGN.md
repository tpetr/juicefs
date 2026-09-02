# Workflow JuiceFS CSI prototype design

## Decision

The prototype is a small node-only CSI driver in an isolated Go module under
`contrib/`. It runs the repository's JuiceFS client as a child process of the
privileged node DaemonSet. `NodePublishVolume` formats or restores SQLite
metadata, mounts JuiceFS on a private host-propagated path, verifies the mount
is readable, bind-mounts it to the kubelet target, and only then returns.

This directly tests the ordering property that the previous FD-passing design
could not provide: gVisor creates its gofer view after the final filesystem is
already present at the CSI target.

The nested module keeps CSI, Kubernetes, and AWS SDK dependencies out of the
main JuiceFS binary. Metadata handoff was implemented only after the fresh
SQLite architecture proof passed from a real `gvisor-netraw` pod in dev2.

## Process mode versus Mount Pods

Process mode has the smallest proof surface and avoids granting pod-creation
RBAC before the mount ordering is known to work. Its important weakness is
that a node-driver restart kills all child mount processes. The POC detects a
mounted target without an in-memory session and fails instead of pretending it
recovered.

The durable design should use one non-gVisor Mount Pod per workspace, pinned to
the requesting node and created with the requesting pod's service account only
after fetching and verifying that pod through the Kubernetes API. That gives
per-workspace resources and AWS identity, and it decouples mounts from driver
rollouts. It requires narrow pod create/get/delete RBAC and careful prevention
of arbitrary service-account selection.

## Reuse assessment

- The Semgrep `fuse-fd-csi-driver` provides useful CSI registration, strict
  kubelet target validation, idempotency, and bounded lifecycle patterns. Its
  defining FD handshake cannot satisfy the required ordering because the FUSE
  server starts in the workload.
- The official JuiceFS CSI driver already supports process and Mount Pod modes,
  but its controller/webhook/Secret/PV feature set is much broader than this
  single-writer workspace protocol. Extending it would couple Semgrep-specific
  success and immutable-generation semantics to a general-purpose driver.
- A small Semgrep-specific driver is therefore the cleanest prototype. A
  production implementation should still reuse or upstream narrowly useful
  official-driver mount helpers rather than copying its full control plane.

## Publish sequence

1. Validate the canonical kubelet target and kubelet-injected pod identity,
   then fetch the actual Pod and cross-check UID, service account, node,
   workspace annotations, and the named control EmptyDir.
2. Derive `juicefs/v1/<tenant>/<deployment>/<workflow>/<workflow UID>` and
   acquire its renewable Kubernetes Lease.
3. Read the exact `metadata/latest.json` object. A 404 or ambiguous 403 uses a
   conditional `If-None-Match: *` initialization; an initialization race is
   followed by another exact read.
4. Create a fresh local SQLite database with `juicefs format`, or download the
   referenced immutable generation and restore it with `juicefs load`.
5. Start `juicefs mount --foreground`, persist its PID, and require a real
   mount-table entry, inode 1, and a successful directory read.
6. Bind mount the private mount to the kubelet target, read it again, create
   the success-intent Unix socket in the Pod's EmptyDir, and return.

The DaemonSet mounts the kubelet pods directory a second time at `/k/pods` so
the socket pathname fits Linux's Unix-domain socket limit. This is the same
hostPath, not an independently selectable path; target validation and the
filesystem bind still use the canonical kubelet path.

## Commit and discard

`workspace-csi-control mark-success` records intent durably but does not unmount.
On unpublish the driver closes the control endpoint, removes the bind mount,
runs `juicefs umount --flush`, and waits for the foreground process. With a
success marker it runs `juicefs dump`, conditionally creates a new immutable
generation, and advances `latest.json` with the prior ETag. Without the marker
it discards local metadata without advancing the pointer. The Lease is released
only after commit or discard completes, and only then is local state removed.

S3 access is limited to exact `GetObject` and conditional `PutObject`. No code
path lists the bucket. Immutable generations use `If-None-Match: *`; pointer
updates use `If-Match` so a stale writer cannot publish.

The DaemonSet sets `JFS_NO_CHECK_OBJECT_STORAGE=1` so `juicefs format` does not
run its generic list-and-temporary-delete storage probe. The driver's exact
pointer initialization verifies the permissions this protocol actually needs.

## Security boundary

The bucket, region, root prefix, tenant, namespace, and service account are
driver configuration, not volume attributes. Deployment and workflow fields
are accepted only as restricted path components and must equal annotations on
the actual Pod returned by the Kubernetes API. Arbitrary host paths, path
traversal, symlink control paths, cross-node requests, and cross-namespace use
are rejected. No command receives credentials as arguments and command output
is bounded.

The dev POC uses the existing `ai-workflows` service account because no
`runtime-demo` service account exists in dev2. Although that role currently
has `s3:ListBucket`, neither the driver nor its manifests invoke a list
operation. Production policy should remove that permission for the workspace
prefix.

Process mode necessarily uses the DaemonSet's AWS identity and therefore does
not prove per-workflow `DeploymentId` session tagging. This is a primary reason
to prefer a non-gVisor Mount Pod using a validated requesting service account
for the durable implementation.

## Recovery boundary and production gaps

The POC persists target paths, workspace identity, generation and ETag state,
success intent, Lease identity, phase, and JuiceFS PID. It deliberately does
not adopt an existing mount after a node-driver restart. A mounted target or
persistent state without the matching in-memory session fails clearly for
operator inspection.

If a successful step loses its node after recording success but before pointer
publication, its Lease eventually expires in an ambiguous `mounted`,
`committing`, `generation-uploaded`, or `committed` phase. A new publisher fails
closed instead of silently restoring the older generation. Production needs a
tested reconciler that can inspect the persisted state and S3 pointer, finish
or reject the commit, and safely clean the old node.

Other production work includes Mount Pod lifecycle and resource limits,
per-workspace AWS identity and session tags, admission-controlled workspace
identity rather than workload-authored annotations, large streaming dump
transfers instead of in-memory S3 bodies, metrics/events, lease-update conflict
retries, garbage collection for unreferenced generations/data chunks, and
end-to-end Argo integration of the generic success call.
