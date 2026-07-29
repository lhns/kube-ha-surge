# 0003 — Gate on real pod readiness, no fixed delays

Status: Accepted

Date: 2026-07-29

## Context

The webhook holds an eviction until the surge pod is "ready to take over." We
have to define "ready" precisely, because it's the exact moment we stop
protecting coverage and let the old pod go. Get it wrong in the optimistic
direction and we allow the eviction before the stand-in is actually serving —
reintroducing the very gap we exist to close. Get it wrong in the pessimistic
direction (wait longer than necessary) and every drain is needlessly slow.

A tempting shortcut is a **fixed delay**: "hold the eviction for N seconds, then
allow." It's trivial to implement (sleep, or reject-until-timestamp) and needs
no knowledge of the pod's state.

## Decision

Gate strictly on the **surge pod's real readiness**, never on a timer:

- The eviction is allowed only once the surge pod's `Ready` condition is true —
  i.e. it has passed its own readiness probe and (where observable) is a serving
  endpoint. We reuse the workload's existing readiness probe as the source of
  truth; the surge pod is a clone, so it has the same probe the app author
  already tuned.
- The only time bound is the **`--max-surge-seconds` fail-open timeout**: if the
  stand-in hasn't become Ready within that window, we *stop holding* and allow
  the eviction anyway ([ADR 0004](0004-opt-in-and-safety-refusals.md)). That's a
  safety valve against a stuck surge wedging a drain — not a substitute for the
  readiness gate.

## Consequences

- **Correct by construction on both ends.** We never allow the eviction before
  the replacement genuinely serves, and we never wait longer than the
  replacement actually takes — the drain is as fast as the app's real startup
  and no faster or slower.
- **Reuses the app author's own definition of "ready."** No new tuning knob; if
  their readiness probe is right for normal rollouts, it's right for surging.
- **A workload with no/meaningless readiness probe degrades to the timeout.** If
  a pod reports Ready instantly (no probe) it may be allowed before it truly
  serves — but that's the app's existing rollout behaviour too, not a regression
  ha-surge introduces. And if it never becomes Ready, `--max-surge-seconds`
  bounds the wait.
- Requires the operator to watch/poll the surge pod's status across eviction
  retries — cheap (eviction QPS is tiny).

## Alternatives considered

- **Fixed delay (hold N seconds).** Rejected: pure guesswork. Shorter than the
  app's real startup → we allow the eviction into a still-cold stand-in and drop
  traffic anyway; longer → every drain crawls for no reason. A fixed delay
  optimises for neither correctness nor speed, and it has to be re-guessed per
  workload. Readiness is the signal a delay is a bad proxy for.
- **Gate on "pod Running" (not Ready).** Rejected: `Running` means the container
  started, not that the app is serving. Readiness is the whole point.
- **Gate on EndpointSlice membership only.** We do read EndpointSlices as
  corroboration that the surge pod is a live endpoint, but the pod's `Ready`
  condition is the primary gate — endpoint programming lags readiness slightly
  and varies by Service/proxy, so requiring it as the sole signal would add
  latency without adding correctness.
