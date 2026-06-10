# ADR 0002 — Host-mediated plugin network access (`net:fetch`)

- **Status:** Proposed
- **Date:** 2026-06-10
- **Related:** [Plugins](../../features/plugins.md), [ADR 0001](0001-plugin-dependency-bundling.md), [ADR 0003](0003-marketplace-trust-tiers.md), [ADR 0004](0004-transaction-document-artifacts.md)

## Context

All three plugin runtimes run with
[**no network access**](../../features/plugins.md#sandbox-limits-v1) — goja
exposes no `fetch`, the WASM module gets a WASI with no sockets, the Lua VM
opens only safe libraries. This is deliberate and it is the reason a plugin is
safe to install.

It is also the single biggest limit on what plugins can be. Every high-value
integration — pull receipts from a document manager, enrich a transaction from
a merchant API, mirror data to another ledger, notify a chat service — needs to
talk to *something*. Today none of that is possible in-process; it can only live
in an external service driven by the [REST API](../../interfaces/rest-api.md)
and [webhooks](../../features/webhooks.md).

The obvious comparison is a desktop editor's plugin marketplace (e.g. Obsidian),
where community plugins have full network and filesystem access, gated only by a
warning and community review. That model works *there* because the blast radius
is one user's notes on one user's desktop. **kasas is different in kind:** it is
a **server** that holds every transaction a user has *and* a
[credential vault](../../getting-started/configuration.md) of live bank tokens.
Server-side network access is **SSRF**: a hostile plugin does not merely phone
home, it can reach the operator's LAN — the router, a NAS, other self-hosted
services, a cloud metadata endpoint. On financial infrastructure, a generic
"⚠️ this plugin uses the network" dialog gets click-through and mostly transfers
liability rather than containing the threat.

So the question is not "network: yes or no." It is "how do we grant network
access in a way that is **declared, narrow, enforced, and observable** — not
blanket and not raw."

## Decision

Add network access as a **declared, host-mediated capability**. A plugin never
opens a socket; it calls a host method, and the **host** performs the request
under rules the host owns — exactly as today's `apply_labels` routes a write
through the capability-checked [host facade](../../features/plugins.md#the-host-api)
rather than letting the VM touch the database.

### 1. A new capability with a manifest-declared allowlist

```toml
capabilities = ["transactions:read", "extensions:write", "net:fetch"]

[net]
# Egress is default-deny. A plugin may only reach hosts it declares here,
# and the gate/dashboard surface this exact list at review and install time.
allow = ["paperless.lan", "api.merchant.example.com"]
```

`net:fetch` without a non-empty `[net].allow` is a manifest error. Because the
egress surface lives **in the manifest**, the install-time prompt can say
*"This plugin talks to: paperless.lan"* — a specific, reviewable claim, not a
generic warning.

### 2. The host performs the request, not the VM

A single host method, mirrored across runtimes like the rest of the host API:

| Runtime | Call |
| --- | --- |
| Lua | `kasas.fetch({ url, method, headers, body, timeout_ms })` |
| JS/TS | `kasas.fetch({ url, method, headers, body, timeoutMs })` |
| Go/WASM | `kasas.Fetch(req)` |

The host:

- checks the `net:fetch` capability up front (host facade, as for every other
  gated method);
- resolves the URL host against the manifest `[net].allow` list — **a request
  to an undeclared host is refused**, not silently dropped;
- applies the SSRF rule below;
- enforces a timeout, a response-size cap, a per-plugin request rate limit, and
  a max redirect count (redirects are re-checked against the allowlist, so a
  permitted host cannot 302 a plugin onto an internal one);
- **logs every egress** (plugin, method, host, status, bytes) for the operator,
  the same way plugin writes are observable through the event stream.

Crucially this keeps the single static binary and the language-agnostic runtime
seam: it is *one* new host method, not a new runtime or a raw socket exposed to
guest code.

### 3. The SSRF rule, tuned for self-hosting

The textbook guard — "block all RFC 1918 / link-local / loopback / metadata
addresses" — would break the **primary** intended use case, because the most
common targets ([paperless-ngx](0004-transaction-document-artifacts.md), a NAS,
a home-lab API) live on the operator's LAN at `192.168.x.x` / `*.lan`.

So the rule is **default-deny private ranges, but operator-grantable per host**:

- Public hosts on the allowlist: permitted (subject to rate/size/timeout).
- Private-range or loopback hosts (RFC 1918, `127.0.0.0/8`, `::1`, link-local,
  and cloud metadata IPs like `169.254.169.254`): **refused unless the operator
  explicitly grants that specific host** for that specific plugin at enable
  time. The grant is per-plugin and per-host, recorded next to the plugin's
  config, and shown on the Plugins page.
- DNS rebinding is defeated by resolving and pinning the address at request time
  and re-validating the *resolved IP* against the grant, not just the hostname.

This is the line between "flag it and allow" (weak) and "the operator grants a
specific, narrow capability to reach a specific host" (defensible).

### 4. Enabling stays admin-only and deliberate

A `net:fetch` plugin is, like every plugin,
[installed disabled and enabled only by an admin](../../features/plugins.md#enabling-is-opt-in-admin-only).
Enabling such a plugin additionally surfaces its declared egress list and
collects any private-host grants from #3 before the plugin loads.

## Consequences

**Gains**

- Unlocks the whole class of integration plugins (importers, enrichers,
  notifiers, mirrors) in-process, without an external companion service.
- The egress surface is **declared and reviewable** before code runs, and
  **logged** while it runs — strictly more containment than a full-trust model.
- No new runtime, no raw sockets in guest code, single static binary preserved.

**Costs & mitigations**

- **A real new attack surface.** Network egress is the capability that turns a
  data-exfiltration bug into data exfiltration. *Mitigation:* host-mediated +
  allowlisted + logged + admin-enabled; and it is gated to a higher
  [trust tier](0003-marketplace-trust-tiers.md) in the registry, never
  auto-listed.
- **An allowlist is not airtight.** A permitted third-party host could itself be
  used as a relay. *Mitigation:* the allowlist shrinks the target set to what
  the operator accepted; egress logs make abuse visible; rate and size caps
  bound the damage.
- **Enforcement must live in the host, not be a runtime courtesy.** If any
  runtime could open a socket directly, the whole control is void.
  *Mitigation:* the sandboxes already deny sockets by construction; `net:fetch`
  is the *only* sanctioned egress, routed through the facade — the
  [submission gate](0003-marketplace-trust-tiers.md) keeps rejecting every other
  network construct exactly as it does today.

## Alternatives considered

- **Stay fully sealed.** Safest, and the status quo. Rejected: it permanently
  forecloses the most valuable plugins and pushes every integration into a
  separate process the operator must run and wire up themselves.
- **Full trust + a warning (the desktop-editor model).** Maximum capability,
  minimum friction. Rejected for a server holding financial data and live
  credentials: a warning does not contain SSRF, and click-through is the norm.
- **Raw socket / unrestricted `fetch` inside the VM.** Simplest for authors.
  Rejected: it removes the host's ability to enforce the allowlist, the SSRF
  rule, rate/size caps, and egress logging — the host could no longer reason
  about what a plugin talks to.
- **Blanket private-IP block (textbook SSRF guard).** Rejected: it breaks the
  self-hosted-LAN target that motivates the feature; the per-host operator grant
  in #3 is the deliberate replacement.
