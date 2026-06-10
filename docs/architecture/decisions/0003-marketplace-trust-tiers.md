# ADR 0003 — Marketplace trust tiers

- **Status:** Proposed
- **Date:** 2026-06-10
- **Related:** [Plugins](../../features/plugins.md), [ADR 0002](0002-plugin-network-capability.md), [kasas-plugins SECURITY.md](https://github.com/paulmeier/kasas-plugins/blob/main/SECURITY.md)

## Context

The registry already tiers plugins by capability: the
[submission gate](https://github.com/paulmeier/kasas-plugins/blob/main/SECURITY.md)
distinguishes read-only plugins from those that can *write* a user's data, and a
maintainer signs off before any write-capable plugin is listed. The published
`index.json` carries a `capability_tier` and the dashboard renders a
[capability-tier warning](../../features/plugins.md#the-community-marketplace) at
install.

Two pressures push this informal tiering toward something explicit:

1. [ADR 0002](0002-plugin-network-capability.md) introduces `net:fetch` —
   qualitatively more dangerous than `labels:write`, because egress turns a bug
   into exfiltration. Folding it into the existing "write" tier would
   under-communicate the jump.
2. [ADR 0001](0001-plugin-dependency-bundling.md) admits bundled third-party
   code, which the gate can prove *structurally* (this artifact is this source)
   but cannot prove *benign*.

A user installing from the marketplace deserves a legible, escalating signal of
how much they are trusting — not a flat list where a coffee-keyword labeler sits
beside a plugin that can read every transaction and POST it to the internet.

## Decision

Make the trust ladder **explicit and visible**, built on the capability set the
host already enforces. Each plugin lands in exactly one tier, derived from its
declared capabilities, and the tier drives both the gate's posture and the
dashboard's presentation.

| Tier | Capabilities it may declare | Gate posture | Dashboard signal |
| --- | --- | --- | --- |
| **Verified** | `transactions:read`, `labels:write`, `extensions:write`, `ui:page` | Static-proven sealed (no network, no fs, no process). Auto-listable once the existing checks pass. | Default. "Sealed: this plugin cannot reach the network or disk." |
| **Connected** | the above **+ `net:fetch`** (with a declared `[net].allow`) | Manual maintainer review **required**; the declared egress list is part of the review and is recorded in the index. | Elevated. Shows the **exact hosts** the plugin may reach; enabling collects any private-host grants ([ADR 0002 §3](0002-plugin-network-capability.md)). |
| **Trusted / Unlisted** | anything broader than the host's reviewed capability set (e.g. a future `transactions:create`, or raw egress) | **Not auto-listed.** Sideload only, with a prominent "you are on your own" posture. | Strongest. "Unreviewed capability surface — install only code you trust." |

Principles that hold across the ladder:

- **The tier is a function of declared capabilities**, computed by the gate, not
  a label an author self-assigns. Declaring `net:fetch` *is* asking for the
  Connected tier and its heavier review.
- **The host still enforces every capability** regardless of tier. The tier is a
  *communication and review* construct layered on top of the existing
  capability checks in the [host facade](../../features/plugins.md#the-host-api);
  it does not replace them.
- **Enabling stays admin-only at every tier.** Tiers change how loudly the risk
  is shown and how hard the gate looks — not the
  [opt-in, disabled-by-default posture](../../features/plugins.md#enabling-is-opt-in-admin-only).
- **WASM remains the recommended runtime for higher tiers**, since its isolation
  is enforced by the memory model and its linear memory is hard-capped — the
  same reasoning the plugin docs already give for untrusted code.

The `index.json` schema gains an explicit `tier` (`verified` | `connected` |
`unlisted`) alongside the existing `capability_tier`, and the
[Marketplace page](../../features/plugins.md#the-community-marketplace) groups
and badges plugins by it.

## Consequences

**Gains**

- A user sees, before installing, *how far* they are extending trust — and for
  Connected plugins, *to which hosts*.
- The gate's effort scales with risk: cheap static proof for Verified, mandatory
  human review for Connected, no auto-listing for Unlisted.
- `net:fetch` ([ADR 0002](0002-plugin-network-capability.md)) gets a home that
  communicates its weight instead of hiding inside "write."

**Costs & mitigations**

- **More maintainer load.** Every Connected plugin needs human review.
  *Mitigation:* that is the point — egress is exactly where human judgment earns
  its keep; Verified plugins still flow through automated checks unchanged.
- **Tier inflation.** Authors may over-declare capabilities and drift upward.
  *Mitigation:* the gate already enforces least privilege; a capability declared
  but unused is a review finding, and tier is computed, not claimed.
- **A tier is a label, not a guarantee.** Connected review proves *what* a
  plugin may reach, not that it is benign with what it reaches.
  *Mitigation:* defense-in-depth from [ADR 0002](0002-plugin-network-capability.md)
  (allowlist, SSRF rule, egress logging, rate/size caps) is what actually
  contains a Connected plugin; the tier just makes the trade legible.

## Alternatives considered

- **Keep the flat capability-tier warning.** Simplest. Rejected: it cannot
  distinguish "labels my transactions" from "reads everything and can POST it
  out," which is precisely the distinction `net:fetch` creates.
- **Per-capability prompts with no tiers.** Granular, but it floods the user with
  individual yes/no decisions and loses the at-a-glance "how exposed am I"
  signal. Rejected in favor of a small, ordered ladder with capabilities rolled
  up into it.
- **Allow Connected plugins to auto-list like Verified.** Lower friction.
  Rejected: egress is the one surface where skipping human review is not worth
  the speed.
