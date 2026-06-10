# ADR 0001 — Allow bundled dependencies in plugins

- **Status:** Proposed
- **Date:** 2026-06-10
- **Related:** [Plugins](../../features/plugins.md), [ADR 0002](0002-plugin-network-capability.md)

## Context

The [plugin model](../../features/plugins.md) today requires **one
self-contained entrypoint** per plugin: "a single, self-contained file (no
`import`/`require`, no `node_modules`)". The registry's
[submission gate](https://github.com/paulmeier/kasas-plugins/blob/main/SECURITY.md)
enforces it — for Lua and JS/TS a static pass rejects module loading outright.

This rule conflates two very different things:

1. **What a plugin can *do*** — reach the network, touch the filesystem, spawn
   a process. This is the trust boundary, and it is the thing the sandbox
   exists to constrain.
2. **What *code* a plugin ships** — whether the developer wrote every line by
   hand or pulled a decimal-math helper, a CSV parser, or a date library off a
   package registry.

Banning `require`/`import` constrains (2) in order to keep (1) simple to
review. But a developer cannot use an off-the-shelf library even when that
library is pure computation that never escapes the sandbox. The result is that
plugin authors re-implement parsers and formatters by hand — more code, more
bugs, in the exact domain (money, dates) where bugs are expensive.

Critically, the JS/TS runtime **already runs esbuild** to strip types and
downlevel syntax at load. esbuild is a *bundler*. The capability to fold
dependencies into a single artifact is already in the host; the rule simply
forbids using it.

## Decision

Decouple "single reviewable artifact" from "no dependencies." A plugin may
depend on third-party libraries, provided those dependencies are **bundled at
submission time into the single entrypoint** that is reviewed, hashed, and
installed.

- **JS/TS.** The author develops against npm packages and ships a
  **pre-bundled** entrypoint (esbuild, in the author's build, mirrored by the
  gate). The reviewer and the host still see one file; the
  [per-file SHA-256 and aggregate content hash](../../features/plugins.md#the-community-marketplace)
  still cover exactly what runs.
- **WASM.** Already true by construction — a compiled module statically links
  its dependencies. This ADR makes the *policy* explicit: a plugin's Go/Rust/Zig
  dependencies are fine because they are sealed inside the module the gate
  compiles and the host instantiates.
- **Lua.** Lowest priority. If demanded, allow a vendored single-file
  concatenation produced by the author, treated like the JS bundle.

The sandbox is **unchanged**. Bundled code runs under the same sealed VM as
hand-written code: no filesystem, no process, no network (network is
[ADR 0002](0002-plugin-network-capability.md), a separate, gated decision). A
bundled `lodash` can sort an array; it cannot open a socket, because the socket
API is not present to call.

## Consequences

**Gains**

- Plugin authors reuse battle-tested libraries for exact-decimal math, parsing,
  and formatting instead of re-implementing them.
- No change to the host's trust model, the capability set, or the single static
  binary. esbuild already ships.
- The integrity story is intact: one artifact, hashed, installed byte-for-byte.

**Costs & mitigations**

- **Reviewability drops.** A bundle is larger and partly machine-generated, so
  a reviewer can no longer read every line on one screen. *Mitigation:* the gate
  requires a linked, buildable source repo and reproduces the bundle from it,
  comparing hashes — review shifts from "read the artifact" to "the artifact is
  provably this source." This mirrors how WASM is already handled (the gate
  compiles, never hand-reads, the module).
- **Supply-chain surface.** A bundled dependency can carry a vulnerability or a
  malicious transitive package. *Mitigation:* the sandbox is the backstop — a
  compromised pure-computation dependency still cannot escape the VM. Network
  egress, the thing that would let a malicious dependency exfiltrate, stays
  gated behind [ADR 0002](0002-plugin-network-capability.md) and is shown at
  install.
- **Bundle bloat.** Nothing forces authors to keep artifacts small.
  *Mitigation:* enforce a per-plugin size bound in the gate (the page-document
  bounds set the precedent for hard limits).

## Alternatives considered

- **Status quo (hand-written single file).** Maximally reviewable, but pushes
  authors to re-implement error-prone primitives and caps what plugins can
  reasonably do. Rejected as the limiting factor on plugin quality.
- **Allow `require`/`node_modules` at runtime.** Lets the host resolve modules
  on disk. Rejected: it reintroduces a module loader inside the sandbox (an
  escape-hatch surface the gate works hard to remove) and breaks the
  one-artifact integrity hash.
