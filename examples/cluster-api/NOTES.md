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

After creating a [new MachineSet via SSA](https://github.com/cahillsf/cluster-api/blob/c587e3e02639b4f6d5504b1cd30395120356f323/internal/controllers/machinedeployment/machinedeployment_sync.go#L179-L203), the controller immediately polls for it:

```go
	if err := ssa.Patch(ctx, r.Client, machineDeploymentManagerName, newMS); err != nil { // kamera -> write recorded as APPLY effect
		r.recorder.Eventf(deployment, corev1.EventTypeWarning, "FailedCreate", "Failed to create MachineSet %s: %v", klog.KObj(newMS), err)
		return nil, errors.Wrapf(err, "failed to create new MachineSet %s", klog.KObj(newMS))
	}
	log.Info(fmt.Sprintf("MachineSet created (%s)", createReason))
	r.recorder.Eventf(deployment, corev1.EventTypeNormal, "SuccessfulCreate", "Created MachineSet %s", klog.KObj(newMS))

	// Keep trying to get the MachineSet. This will force the cache to update and prevent any future reconciliation of
	// the MachineDeployment to reconcile with an outdated list of MachineSets which could lead to unwanted creation of
	// a duplicate MachineSet.
	var pollErrors []error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		ms := &clusterv1.MachineSet{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(newMS), ms); err != nil { // kameria -> reads frozen frame → 404
			// Do not return error here. Continue to poll even if we hit an error
			// so that we avoid existing because of transient errors like network flakes.
			// Capture all the errors and return the aggregate error if the poll fails eventually.
			pollErrors = append(pollErrors, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return nil, errors.Wrapf(kerrors.NewAggregate(pollErrors), "failed to get the MachineSet %s after creation", klog.KObj(newMS))
	}
	return newMS, nil
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
