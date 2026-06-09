# cluster-api kamera example — design notes

## Why MachineDeployment doesn't work out of the box

### kamera's execution model

kamera gives each reconcile call a **frozen snapshot** of the world to read from
(`replay.CacheFrame`). Writes made during the reconcile — `Create`, `Update`,
`Patch`, SSA `Apply` — are recorded as *effects* and applied **after** the
reconcile returns to produce the next state node. Reads and writes go to
completely separate stores:

```
Get/List  →  reads CacheFrame (frozen for the duration of the reconcile)
Create/Patch/Apply  →  appended to EffectRecorder (not visible in CacheFrame)
```

This is intentional: it lets kamera explore all possible orderings of concurrent
reconcilers without their in-flight writes contaminating each other's reads.

### What MachineDeployment does

After creating a new MachineSet via SSA, the controller immediately polls for it:

```go
// machinedeployment_sync.go
ssa.Patch(ctx, r.Client, fieldManager, newMS)   // write recorded as APPLY effect

wait.PollUntilContextTimeout(ctx, 100ms, 10s, func(ctx context.Context) (bool, error) {
    return r.Client.Get(ctx, client.ObjectKeyFromObject(newMS), ms)  // reads frozen frame → 404
})
// times out after 10 seconds, returns error
```

The poll is there in production to wait for the informer cache to catch up with
the API server after a write. In kamera, there is no cache lag — but the write
also isn't in the frame yet. The reconcile is blocked waiting for a write that
won't be flushed until the reconcile unblocks: a deadlock.

### Why this isn't an SSA gap

kamera fully records SSA operations (as `event.APPLY`). The problem is not SSA
specifically — a regular `Create` followed by `Get` in the same reconcile would
hit the same wall. The issue is read-after-write within a single reconcile, which
the snapshot model deliberately prevents.

### What would fix it

A client wrapper that maintains a per-reconcile write buffer and checks it before
falling through to the frozen frame would allow read-after-write. This would be a
kamera core change, not something that belongs in individual examples or in the
upstream controller.

### Controller choice for this example

---

## The `replace` directive blocks upstream merge

`go.mod` contains:

```
replace sigs.k8s.io/cluster-api => /Users/stephen.cahill/cluster-api-fork/cluster-api
```

This is an absolute local path. Go modules forbid absolute-path `replace` directives
in published modules, so this example **cannot be merged to the upstream kamera repo
as-is**. The same constraint applies to the other out-of-tree examples (karpenter uses
a local replace too).

The options when the time comes:
- Tag a release of the cluster-api fork and replace with a versioned reference, or
- Keep this example as a local-only harness that lives outside the kamera module tree
  (the approach used before this PR, where `examples/cluster-api` was `.gitignore`d).

Also related: the module is declared as `sigs.k8s.io/cluster-api/examples/kamera`
(not under the kamera module path) to satisfy Go's `internal` package restriction.
That too would need revisiting for any upstream contribution.

---

### Controller choice for this example

`clusterresourcesetbinding.Reconciler` avoids this entirely: it reads a
`ClusterResourceSetBinding`, patches it once, reads the owning `Cluster`, and
either deletes the binding or returns. No read-after-write, no polling, no
unexported fields that need a constructor.
