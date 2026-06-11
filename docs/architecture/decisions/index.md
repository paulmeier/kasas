# Architecture Decision Records

An **Architecture Decision Record (ADR)** captures a single significant
decision — the context that forced it, the choice made, and the consequences
accepted — so the reasoning survives the commit that implements it.

These records are intentionally **append-only**. A decision that no longer
holds is not edited away; a later ADR supersedes it and the old one stays as
history, its status updated to point forward.

## Status vocabulary

| Status | Meaning |
| --- | --- |
| **Proposed** | Written up for discussion; not yet agreed or built. |
| **Accepted** | Agreed and in force (built, or committed to build). |
| **Superseded** | Replaced by a later ADR (linked from the header). |
| **Rejected** | Considered and deliberately not taken (kept for the reasoning). |

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-plugin-dependency-bundling.md) | Allow bundled dependencies in plugins | Accepted |
| [0002](0002-plugin-network-capability.md) | Host-mediated plugin network access (`net:fetch`) | Accepted |
| [0003](0003-marketplace-trust-tiers.md) | Marketplace trust tiers | Accepted |
| [0004](0004-transaction-document-artifacts.md) | Document & artifact association for transactions | Proposed |

ADRs 0001–0003 form one arc: they widen what a plugin may *do* without
abandoning the sandbox that makes plugins safe to install. ADR 0004 is the
use case that motivated the arc — associating a receipt with a transaction —
and shows how the new seams (plus the existing ones) compose to serve it.
