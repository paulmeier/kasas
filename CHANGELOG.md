# Changelog

## [3.2.0](https://github.com/paulmeier/kasas/compare/v3.1.4...v3.2.0) (2026-07-08)


### Features

* migrate a SQLite ledger to Postgres from the CLI or dashboard ([#170](https://github.com/paulmeier/kasas/issues/170)) ([89d6cd0](https://github.com/paulmeier/kasas/commit/89d6cd0b0ff184e410e177465669806b403a3be0))


### Dependencies

* bump github.com/pressly/goose/v3 in the go-modules group ([#169](https://github.com/paulmeier/kasas/issues/169)) ([16c812f](https://github.com/paulmeier/kasas/commit/16c812f54c1ee27f0159aebf686314ad57a7782e))

## [3.1.4](https://github.com/paulmeier/kasas/compare/v3.1.3...v3.1.4) (2026-06-26)


### Documentation

* **adr:** add a Syncthing-model community relay tier to ADR 0010 ([#167](https://github.com/paulmeier/kasas/issues/167)) ([b9a7c7d](https://github.com/paulmeier/kasas/commit/b9a7c7d08e0cf2cb544784afca7642fb2eef2a7b))

## [3.1.3](https://github.com/paulmeier/kasas/compare/v3.1.2...v3.1.3) (2026-06-26)


### Documentation

* **adr:** revise ADR 0010 to custody-free peer connectivity (Syncthing-model) ([#165](https://github.com/paulmeier/kasas/issues/165)) ([b3fab1d](https://github.com/paulmeier/kasas/commit/b3fab1d190e824ba7df98952f2a5467a7c2b861d))

## [3.1.2](https://github.com/paulmeier/kasas/compare/v3.1.1...v3.1.2) (2026-06-26)


### Documentation

* **adr:** add ADR 0010 — hosted peer directory & zero-knowledge relay ([#163](https://github.com/paulmeier/kasas/issues/163)) ([3a32821](https://github.com/paulmeier/kasas/commit/3a328218b22c5909dcd1f0a7d45192eb55b00440))

## [3.1.1](https://github.com/paulmeier/kasas/compare/v3.1.0...v3.1.1) (2026-06-26)


### Documentation

* **adr:** add ADR 0009 — selective peer-to-peer ledger sharing ([#161](https://github.com/paulmeier/kasas/issues/161)) ([a55713e](https://github.com/paulmeier/kasas/commit/a55713e667898e10ae7dc33b6c61525b408c47a3))

## [3.1.0](https://github.com/paulmeier/kasas/compare/v3.0.4...v3.1.0) (2026-06-26)


### Features

* **sources:** add inbound-webhook ingestion source ([#159](https://github.com/paulmeier/kasas/issues/159)) ([e58228f](https://github.com/paulmeier/kasas/commit/e58228f795c08c73afb0a2e9aea6c0bec3d67e15))


### Dependencies

* bump the go-modules group with 2 updates ([#158](https://github.com/paulmeier/kasas/issues/158)) ([0b58019](https://github.com/paulmeier/kasas/commit/0b58019d848d2ad197cfa6f5384e916379fdb54a))

## [3.0.4](https://github.com/paulmeier/kasas/compare/v3.0.3...v3.0.4) (2026-06-26)


### Documentation

* **mcp:** expand the remote-server (HTTP) connection guidance ([#156](https://github.com/paulmeier/kasas/issues/156)) ([357fcb6](https://github.com/paulmeier/kasas/commit/357fcb648d63f4617a6bf748db001acca23c2573))

## [3.0.3](https://github.com/paulmeier/kasas/compare/v3.0.2...v3.0.3) (2026-06-26)


### Documentation

* **mcp:** document connecting Claude Desktop, Hermes, and OpenClaw ([#154](https://github.com/paulmeier/kasas/issues/154)) ([a58fc76](https://github.com/paulmeier/kasas/commit/a58fc76e1fc144ee8008385bcf4c239b2de25e09))
* **mcp:** expand the remote-server (HTTP) connection guidance ([c4fc95a](https://github.com/paulmeier/kasas/commit/c4fc95ab00891704c40a7fae16f23e4626623b17))

## [3.0.2](https://github.com/paulmeier/kasas/compare/v3.0.1...v3.0.2) (2026-06-26)


### Bug Fixes

* **simplefin:** clamp lookback so the bridge stops warning every sync ([#152](https://github.com/paulmeier/kasas/issues/152)) ([7d601e2](https://github.com/paulmeier/kasas/commit/7d601e28dea02b1884d79205f727a837b3f6445f))

## [3.0.1](https://github.com/paulmeier/kasas/compare/v3.0.0...v3.0.1) (2026-06-22)


### Bug Fixes

* **dashboard:** hint instead of error when webhooks need a token ([#150](https://github.com/paulmeier/kasas/issues/150)) ([f71e0d7](https://github.com/paulmeier/kasas/commit/f71e0d79052dcf845589ba22d893536d6c4e71f3))

## [3.0.0](https://github.com/paulmeier/kasas/compare/v2.35.1...v3.0.0) (2026-06-22)


### ⚠ BREAKING CHANGES

* **security:** kasas now refuses to start when server.addr binds beyond loopback (e.g. the default :8080) with no dashboard token set. Set dashboard.token / KASAS_DASHBOARD_TOKEN, bind server.addr to 127.0.0.1, or set server.allow_unauthenticated=true (KASAS_SERVER_ALLOW_UNAUTHENTICATED=true) to run unauthenticated on purpose; the official Docker image and docker-compose.yml ship this opt-in so `docker compose up` is unaffected. The admin/code-execution operations (plugin enable, self-update, API-key/webhook/settings management, restart, MCP-over-HTTP, plugin page render/action) now require the dashboard token and return HTTP 503 on an unsecured instance, and update.allow_apply now defaults to false.

### Bug Fixes

* **security:** require a dashboard token for admin ops; refuse to start exposed-unauthenticated ([#148](https://github.com/paulmeier/kasas/issues/148)) ([d7ec23c](https://github.com/paulmeier/kasas/commit/d7ec23c94ff954e0220204eb7b323550d4b457d7))

## [2.35.1](https://github.com/paulmeier/kasas/compare/v2.35.0...v2.35.1) (2026-06-21)


### Bug Fixes

* **deps:** bump Go 1.25.11 + x/net + go-jose to clear 11 reachable CVEs ([#146](https://github.com/paulmeier/kasas/issues/146)) ([05a90af](https://github.com/paulmeier/kasas/commit/05a90af20b24ae58688c8e599c89b430b84248a6))

## [2.35.0](https://github.com/paulmeier/kasas/compare/v2.34.0...v2.35.0) (2026-06-21)


### Features

* **plugins:** OnTransactionDelete hook + soft-delete ADR (0007) ([#144](https://github.com/paulmeier/kasas/issues/144)) ([645dcdb](https://github.com/paulmeier/kasas/commit/645dcdb26f876478a3c775a0b5322283fd846e34))

## [2.34.0](https://github.com/paulmeier/kasas/compare/v2.33.0...v2.34.0) (2026-06-21)


### Features

* plugins can provide ingestion sources (ADR 0005, source:provide) ([#142](https://github.com/paulmeier/kasas/issues/142)) ([53f0694](https://github.com/paulmeier/kasas/commit/53f06946df8694576639826f184155d90a06e15c))

## [2.33.0](https://github.com/paulmeier/kasas/compare/v2.32.0...v2.33.0) (2026-06-21)


### Features

* **dashboard:** sources as an icon list with per-source detail pages ([#141](https://github.com/paulmeier/kasas/issues/141)) ([14ff0c7](https://github.com/paulmeier/kasas/commit/14ff0c7e752eb1c1bf6b9f8de979d4c693bd51a1))


### Dependencies

* bump the go-modules group with 3 updates ([#138](https://github.com/paulmeier/kasas/issues/138)) ([a0bd887](https://github.com/paulmeier/kasas/commit/a0bd887a3a0664335adf042cc43e2bd66ddd1659))

## [2.32.0](https://github.com/paulmeier/kasas/compare/v2.31.0...v2.32.0) (2026-06-13)


### Features

* publish windows/amd64 release binaries ([#136](https://github.com/paulmeier/kasas/issues/136)) ([763de68](https://github.com/paulmeier/kasas/commit/763de68eac1b445ec973b3938e41b8becd5d1a33))

## [2.31.0](https://github.com/paulmeier/kasas/compare/v2.30.2...v2.31.0) (2026-06-13)


### Features

* **dashboard:** enter relationship target by ID with live validation ([#134](https://github.com/paulmeier/kasas/issues/134)) ([5ed921c](https://github.com/paulmeier/kasas/commit/5ed921cfbfd9267d8dbfbef6d79132d436ffd856))

## [2.30.2](https://github.com/paulmeier/kasas/compare/v2.30.1...v2.30.2) (2026-06-13)


### Bug Fixes

* report Fresh=false deterministically for a stale market read ([#132](https://github.com/paulmeier/kasas/issues/132)) ([9ab175a](https://github.com/paulmeier/kasas/commit/9ab175ae248e8571bb6a1c35ef8a80e3369d149b))

## [2.30.1](https://github.com/paulmeier/kasas/compare/v2.30.0...v2.30.1) (2026-06-13)


### Bug Fixes

* don't warm the market cache on "sync all" ([#130](https://github.com/paulmeier/kasas/issues/130)) ([6508343](https://github.com/paulmeier/kasas/commit/6508343352605b4520baec856f4095e289108cfe))

## [2.30.0](https://github.com/paulmeier/kasas/compare/v2.29.2...v2.30.0) (2026-06-12)


### Features

* external market & reference data source (ADR 0006, Alpha Vantage) ([#127](https://github.com/paulmeier/kasas/issues/127)) ([caf15d2](https://github.com/paulmeier/kasas/commit/caf15d27fb551cc34c19e825fea282449a49e65c))


### Documentation

* fix broken market.md config link breaking the strict build ([#129](https://github.com/paulmeier/kasas/issues/129)) ([cb34349](https://github.com/paulmeier/kasas/commit/cb34349fe9bdc131a79055c86a84e13ac7e7c617))

## [2.29.2](https://github.com/paulmeier/kasas/compare/v2.29.1...v2.29.2) (2026-06-12)


### Documentation

* add ADR 0006 for external market and reference data ([#125](https://github.com/paulmeier/kasas/issues/125)) ([e5e985d](https://github.com/paulmeier/kasas/commit/e5e985d28b496952fd26c7517c4f12ec23be6fbe))

## [2.29.1](https://github.com/paulmeier/kasas/compare/v2.29.0...v2.29.1) (2026-06-12)


### Dependencies

* bump the go-modules group with 4 updates ([#123](https://github.com/paulmeier/kasas/issues/123)) ([5a50744](https://github.com/paulmeier/kasas/commit/5a5074494b52759ee0138eeccedfdde09b7405fa))

## [2.29.0](https://github.com/paulmeier/kasas/compare/v2.28.1...v2.29.0) (2026-06-11)


### Features

* add Contributor License Agreement and Commercial License documentation ([#118](https://github.com/paulmeier/kasas/issues/118)) ([479e16d](https://github.com/paulmeier/kasas/commit/479e16d23a69f27b176573a9c604539e6dbe2cb3))


### Bug Fixes

* **ci:** store CLA signatures on unprotected branch; exempt bots ([#120](https://github.com/paulmeier/kasas/issues/120)) ([be56952](https://github.com/paulmeier/kasas/commit/be569527c604e21b3364116f9a256edffabb86e6))

## [2.28.1](https://github.com/paulmeier/kasas/compare/v2.28.0...v2.28.1) (2026-06-11)


### Documentation

* add ADR 0005 — plugin-originated transactions (source:provide) ([#116](https://github.com/paulmeier/kasas/issues/116)) ([18d6463](https://github.com/paulmeier/kasas/commit/18d6463a139e426b88a9c0f70adb0308e3605c9c))

## [2.28.0](https://github.com/paulmeier/kasas/compare/v2.27.1...v2.28.0) (2026-06-11)


### Features

* implement ADR 0003 — marketplace trust tiers (host side) ([#114](https://github.com/paulmeier/kasas/issues/114)) ([dcc0350](https://github.com/paulmeier/kasas/commit/dcc035084199e93bfb1db92cd4cff8d11852a715))

## [2.27.1](https://github.com/paulmeier/kasas/compare/v2.27.0...v2.27.1) (2026-06-11)


### Documentation

* update egress caps configuration link to point to the correct settings documentation ([#112](https://github.com/paulmeier/kasas/issues/112)) ([f18e718](https://github.com/paulmeier/kasas/commit/f18e7184762180047d81e88d4bfec3f0412eed34))

## [2.27.0](https://github.com/paulmeier/kasas/compare/v2.26.2...v2.27.0) (2026-06-11)


### Features

* implement ADR 0002 — host-mediated plugin network access (net:fetch) ([#110](https://github.com/paulmeier/kasas/issues/110)) ([e99d170](https://github.com/paulmeier/kasas/commit/e99d170dc8765bc4917a0d7bf342cfa5d10f1f8e))

## [2.26.2](https://github.com/paulmeier/kasas/compare/v2.26.1...v2.26.2) (2026-06-11)


### Bug Fixes

* marketplace error when plugins disabled + seed config.toml for Docker ([#108](https://github.com/paulmeier/kasas/issues/108)) ([d526dee](https://github.com/paulmeier/kasas/commit/d526dee20c7924526f5888cd44c1c7ed0dc4e3b6))

## [2.26.1](https://github.com/paulmeier/kasas/compare/v2.26.0...v2.26.1) (2026-06-10)


### Documentation

* add ADRs for plugin network access and transaction artifacts ([#105](https://github.com/paulmeier/kasas/issues/105)) ([27c7c0a](https://github.com/paulmeier/kasas/commit/27c7c0a4749dcf7d7daaaa51f2fd8f44b511175b))
* **sources:** make crypto address-watching + Sync now discoverable ([#103](https://github.com/paulmeier/kasas/issues/103)) ([3185d2c](https://github.com/paulmeier/kasas/commit/3185d2c8783703f016957aace8f2fad14800b1fa))

## [2.26.0](https://github.com/paulmeier/kasas/compare/v2.25.1...v2.26.0) (2026-06-10)


### Features

* **settings:** edit kasas settings and source config from the dashboard, persisted permanently ([#101](https://github.com/paulmeier/kasas/issues/101)) ([13facf1](https://github.com/paulmeier/kasas/commit/13facf110fbbc308dd2542afe3d3b62e8b987467))

## [2.25.1](https://github.com/paulmeier/kasas/compare/v2.25.0...v2.25.1) (2026-06-10)


### Bug Fixes

* **plugins:** raise marketplace download limits so wasm plugins can install ([#99](https://github.com/paulmeier/kasas/issues/99)) ([6ed81ed](https://github.com/paulmeier/kasas/commit/6ed81ed22b4a7d52f68171f64cd5adae6f78fffe))

## [2.25.0](https://github.com/paulmeier/kasas/compare/v2.24.1...v2.25.0) (2026-06-09)


### Features

* **plugins:** Go (WASM) plugin runtime via wazero + guest SDK ([#97](https://github.com/paulmeier/kasas/issues/97)) ([8ce9cf7](https://github.com/paulmeier/kasas/commit/8ce9cf72e0b415e1ed895038f68023a7067db459))


### Bug Fixes

* **dashboard:** a full-document GET of /ext/&lt;name&gt; (hard refresh, ([8ce9cf7](https://github.com/paulmeier/kasas/commit/8ce9cf72e0b415e1ed895038f68023a7067db459))

## [2.24.1](https://github.com/paulmeier/kasas/compare/v2.24.0...v2.24.1) (2026-06-09)


### Bug Fixes

* **plugins:** empty Lua tables in page docs, and real JS Dates for plugins ([#95](https://github.com/paulmeier/kasas/issues/95)) ([b386c44](https://github.com/paulmeier/kasas/commit/b386c44a5fcba376a10e36ed3b1c2e4eac7b42ff))

## [2.24.0](https://github.com/paulmeier/kasas/compare/v2.23.0...v2.24.0) (2026-06-09)


### Features

* **plugins:** user-configurable plugin settings via config TOML and dashboard forms ([#93](https://github.com/paulmeier/kasas/issues/93)) ([d95d58e](https://github.com/paulmeier/kasas/commit/d95d58e84d1675f39f9c17324a4f12b0cb28ecd3))

## [2.23.0](https://github.com/paulmeier/kasas/compare/v2.22.0...v2.23.0) (2026-06-09)


### Features

* **plugins:** let plugins extend the dashboard with a sidebar entry and page ([#91](https://github.com/paulmeier/kasas/issues/91)) ([7b296e9](https://github.com/paulmeier/kasas/commit/7b296e959cdd2c408f3148d4de5f8fabdab93c55))

## [2.22.0](https://github.com/paulmeier/kasas/compare/v2.21.0...v2.22.0) (2026-06-09)


### Features

* uninstall plugins from the dashboard with an OnUninstall cleanup hook ([#89](https://github.com/paulmeier/kasas/issues/89)) ([fc79ef0](https://github.com/paulmeier/kasas/commit/fc79ef03f67398a65fadcf3c57443ee40f53b75c))

## [2.21.0](https://github.com/paulmeier/kasas/compare/v2.20.0...v2.21.0) (2026-06-09)


### Features

* add the community plugin marketplace (browse and install community plugins) ([#87](https://github.com/paulmeier/kasas/issues/87)) ([0655148](https://github.com/paulmeier/kasas/commit/06551486b869f4b8dde54b473b17e397e9f2947b))

## [2.20.0](https://github.com/paulmeier/kasas/compare/v2.19.0...v2.20.0) (2026-06-09)


### Features

* **plugins:** add JavaScript/TypeScript plugin runtime (goja + esbuild) ([#84](https://github.com/paulmeier/kasas/issues/84)) ([44e069a](https://github.com/paulmeier/kasas/commit/44e069a36ac8e554b946e059286cdc8d84bc3277))

## [2.19.0](https://github.com/paulmeier/kasas/compare/v2.18.0...v2.19.0) (2026-06-09)


### Features

* add Bitcoin and Ethereum address-watching sources ([#82](https://github.com/paulmeier/kasas/issues/82)) ([6aeb533](https://github.com/paulmeier/kasas/commit/6aeb5333078f6e6936cf10cc89b174c0317a4fad))

## [2.18.0](https://github.com/paulmeier/kasas/compare/v2.17.0...v2.18.0) (2026-06-09)


### Features

* add Plaid as a data source with multi-bank fan-out ([#80](https://github.com/paulmeier/kasas/issues/80)) ([8d03d3c](https://github.com/paulmeier/kasas/commit/8d03d3c5e0e19623b07c0c314ebba024b14b1020))

## [2.17.0](https://github.com/paulmeier/kasas/compare/v2.16.0...v2.17.0) (2026-06-09)


### Features

* add Teller as a data source with multi-bank fan-out ([#78](https://github.com/paulmeier/kasas/issues/78)) ([fe613f2](https://github.com/paulmeier/kasas/commit/fe613f2cf500e4cfd307bd968cfe496bb7e69b1d))

## [2.16.0](https://github.com/paulmeier/kasas/compare/v2.15.1...v2.16.0) (2026-06-08)


### Features

* add CSV file import source (local folder + Google Drive) ([#76](https://github.com/paulmeier/kasas/issues/76)) ([9092d0c](https://github.com/paulmeier/kasas/commit/9092d0c3615c6e4e21dbba34abcdf137b70a6196))

## [2.15.1](https://github.com/paulmeier/kasas/compare/v2.15.0...v2.15.1) (2026-06-08)


### Bug Fixes

* apply dashboard updates instead of serving the stale cached UI ([#74](https://github.com/paulmeier/kasas/issues/74)) ([ce4ccd6](https://github.com/paulmeier/kasas/commit/ce4ccd67113970abc2284656d3698601135ce133))

## [2.15.0](https://github.com/paulmeier/kasas/compare/v2.14.2...v2.15.0) (2026-06-08)


### Features

* let rules apply schema extensions, not just labels ([#72](https://github.com/paulmeier/kasas/issues/72)) ([deeb288](https://github.com/paulmeier/kasas/commit/deeb288f68fdae9a15fd6738a9807342451712df))

## [2.14.2](https://github.com/paulmeier/kasas/compare/v2.14.1...v2.14.2) (2026-06-08)


### Bug Fixes

* handle a disabled plugin system and show event details in a modal ([#70](https://github.com/paulmeier/kasas/issues/70)) ([a50c0f9](https://github.com/paulmeier/kasas/commit/a50c0f972bbf2b917d7c76f3f93584557d28dcbd))

## [2.14.1](https://github.com/paulmeier/kasas/compare/v2.14.0...v2.14.1) (2026-06-07)


### Documentation

* replace README architecture diagram with an embedded dashboard demo ([#68](https://github.com/paulmeier/kasas/issues/68)) ([4bf8662](https://github.com/paulmeier/kasas/commit/4bf8662dd2bf9d301f9467e82f357fee15ada5b8))

## [2.14.0](https://github.com/paulmeier/kasas/compare/v2.13.1...v2.14.0) (2026-06-07)


### Features

* split the dashboard into Transactions and Accounts pages ([#66](https://github.com/paulmeier/kasas/issues/66)) ([4eab536](https://github.com/paulmeier/kasas/commit/4eab5368b3f93aa4cb949b086f2b0f6dfd00b47d))

## [2.13.1](https://github.com/paulmeier/kasas/compare/v2.13.0...v2.13.1) (2026-06-07)


### Documentation

* fix non-rendering Mermaid diagrams and add AI-first-class philosophy section ([#63](https://github.com/paulmeier/kasas/issues/63)) ([af23277](https://github.com/paulmeier/kasas/commit/af23277af1dbe8b4f8c9e941a9a299813a64f4a9))

## [2.13.0](https://github.com/paulmeier/kasas/compare/v2.12.0...v2.13.0) (2026-06-07)


### Features

* manual entry — create, edit, and delete transactions and accounts by hand ([#61](https://github.com/paulmeier/kasas/issues/61)) ([898c394](https://github.com/paulmeier/kasas/commit/898c39476619d602806a432030be060d80c544ad))

## [2.12.0](https://github.com/paulmeier/kasas/compare/v2.11.1...v2.12.0) (2026-06-07)


### Features

* transaction relationships — explicit directed edges between transactions ([#59](https://github.com/paulmeier/kasas/issues/59)) ([47d3408](https://github.com/paulmeier/kasas/commit/47d3408675a4bcd7500caa5aa30df7aba3855d2b))

## [2.11.1](https://github.com/paulmeier/kasas/compare/v2.11.0...v2.11.1) (2026-06-07)


### Documentation

* document pluggable ingestion sources (SimpleFIN as the first, not the only) ([#57](https://github.com/paulmeier/kasas/issues/57)) ([df66de6](https://github.com/paulmeier/kasas/commit/df66de652bfac10f66474d39e70483968de28324))

## [2.11.0](https://github.com/paulmeier/kasas/compare/v2.10.0...v2.11.0) (2026-06-07)


### Features

* pluggable ingestion sources — a Source SDK with SimpleFIN as the first ([#55](https://github.com/paulmeier/kasas/issues/55)) ([88f07b8](https://github.com/paulmeier/kasas/commit/88f07b85babff2dab215c8bd4a9aea685f470a62))

## [2.10.0](https://github.com/paulmeier/kasas/compare/v2.9.0...v2.10.0) (2026-06-07)


### Features

* transaction provenance — a derived lineage view per transaction ([#53](https://github.com/paulmeier/kasas/issues/53)) ([6ce78ba](https://github.com/paulmeier/kasas/commit/6ce78bafea73304b31ec89b71eb2a9815aeee345))

## [2.9.0](https://github.com/paulmeier/kasas/compare/v2.8.0...v2.9.0) (2026-06-06)


### Features

* plugin system — sandboxed Lua plugins that react to ledger events ([#48](https://github.com/paulmeier/kasas/issues/48)) ([da36018](https://github.com/paulmeier/kasas/commit/da36018f9f91189616114d2a89044aa70867eb17))

## [2.8.0](https://github.com/paulmeier/kasas/compare/v2.7.0...v2.8.0) (2026-06-06)


### Features

* schema extensions (arbitrary namespaced transaction metadata) ([#46](https://github.com/paulmeier/kasas/issues/46)) ([1ad0c91](https://github.com/paulmeier/kasas/commit/1ad0c9197385f48f07a6295ff57dd876387b125c))

## [2.7.0](https://github.com/paulmeier/kasas/compare/v2.6.0...v2.7.0) (2026-06-06)


### Features

* webhooks + scoped API key provisioning ([#44](https://github.com/paulmeier/kasas/issues/44)) ([15de699](https://github.com/paulmeier/kasas/commit/15de699eb5cf24e5119b03cbea778e7a65ddcb97))

## [2.6.0](https://github.com/paulmeier/kasas/compare/v2.5.0...v2.6.0) (2026-06-06)


### Features

* immutable transaction history (full-snapshot versions, REST/MCP/dashboard) ([#42](https://github.com/paulmeier/kasas/issues/42)) ([e8c54db](https://github.com/paulmeier/kasas/commit/e8c54db90eb1956364c66a00a4f1c2d4cc26c395))

## [2.5.0](https://github.com/paulmeier/kasas/compare/v2.4.0...v2.5.0) (2026-06-06)


### Features

* canonical event stream (REST cursor, live SSE, MCP, dashboard) ([#40](https://github.com/paulmeier/kasas/issues/40)) ([4d175b7](https://github.com/paulmeier/kasas/commit/4d175b7f28f7161f5f7062e64ec66531b898b97c))

## [2.4.0](https://github.com/paulmeier/kasas/compare/v2.3.0...v2.4.0) (2026-06-06)


### Features

* authenticate the API, dashboard, and MCP with a dashboard token ([#38](https://github.com/paulmeier/kasas/issues/38)) ([30322c1](https://github.com/paulmeier/kasas/commit/30322c1b3e0225d630aed798ec465491259c75cd))

## [2.3.0](https://github.com/paulmeier/kasas/compare/v2.2.0...v2.3.0) (2026-06-06)


### Features

* rules engine to auto-label transactions matching a search query ([#36](https://github.com/paulmeier/kasas/issues/36)) ([17b2ce9](https://github.com/paulmeier/kasas/commit/17b2ce960379514ed80e73413ad104d0a19be066))

## [2.2.0](https://github.com/paulmeier/kasas/compare/v2.1.0...v2.2.0) (2026-06-06)


### Features

* transaction search page with shared REST + MCP query language ([#34](https://github.com/paulmeier/kasas/issues/34)) ([5e1686a](https://github.com/paulmeier/kasas/commit/5e1686a9eed46347d107191de5ca94f945ea6a5a))

## [2.1.0](https://github.com/paulmeier/kasas/compare/v2.0.0...v2.1.0) (2026-06-06)


### Features

* dashboard Settings page (SimpleFIN connect, force-sync, label-safe refresh) ([#32](https://github.com/paulmeier/kasas/issues/32)) ([386a7a1](https://github.com/paulmeier/kasas/commit/386a7a1a0b4b3a87392aa85551edc7bb573f67dd))

## [2.0.0](https://github.com/paulmeier/kasas/compare/v1.6.0...v2.0.0) (2026-06-06)


### ⚠ BREAKING CHANGES

* transaction "tags" (a JSON array) are replaced by "labels", strict key:value pairs (a JSON object). The DB column is renamed and existing tag data is cleared; /api/v1/tags* endpoints are replaced by /api/v1/labels*, and the transaction DTO field "tags" ([]string) becomes "labels" (object).

### Features

* ship key:value labels (replaces tags) ([#30](https://github.com/paulmeier/kasas/issues/30)) ([6137b92](https://github.com/paulmeier/kasas/commit/6137b9268bd728ab9aec0f5fc3400d1396efad84))

## [1.6.0](https://github.com/paulmeier/kasas/compare/v1.5.0...v1.6.0) (2026-06-05)


### Features

* add collapsible sidebar nav with Tags management page ([9c8e4cc](https://github.com/paulmeier/kasas/commit/9c8e4cc7e6d49348febe463b7b948a22cc9a8bbb))

## [1.5.0](https://github.com/paulmeier/kasas/compare/v1.4.0...v1.5.0) (2026-06-05)


### Features

* add editable transaction tags with typeahead ([4caae80](https://github.com/paulmeier/kasas/commit/4caae80f9de02c8236a4534d5c1a142024d72b88))
* add editable transaction tags with typeahead ([04bf337](https://github.com/paulmeier/kasas/commit/04bf337974265585b9dd137080909900ccb3fd04))

## [1.4.0](https://github.com/paulmeier/kasas/compare/v1.3.1...v1.4.0) (2026-06-05)


### Features

* **dashboard:** add build-version badge and fix wasm cache staleness ([5eb1c9c](https://github.com/paulmeier/kasas/commit/5eb1c9ccbbfd4489e0ed1833fc3bfc7aacede793))
* **dashboard:** add build-version badge and fix wasm cache staleness ([b2a419f](https://github.com/paulmeier/kasas/commit/b2a419f9464fef9cf9ca8d21f445214b4ba76a51))

## [1.3.1](https://github.com/paulmeier/kasas/compare/v1.3.0...v1.3.1) (2026-06-05)


### Bug Fixes

* **dashboard:** bust the service-worker cache when the UI changes ([6bf3456](https://github.com/paulmeier/kasas/commit/6bf34561b693fefd3829416fdc4e8bc9b9cb7b4c))
* **dashboard:** bust the service-worker cache when the UI changes ([23ab33b](https://github.com/paulmeier/kasas/commit/23ab33b2b02f2a31210ea3f7a6968c147c9e9269))

## [1.3.0](https://github.com/paulmeier/kasas/compare/v1.2.1...v1.3.0) (2026-06-05)


### Features

* **dashboard:** sortable columns, page-size selector, and pagination ([37138cd](https://github.com/paulmeier/kasas/commit/37138cd0a0ce635a5518e7bcaaba906342c2e492))
* **dashboard:** sortable columns, page-size selector, and pagination ([cbde367](https://github.com/paulmeier/kasas/commit/cbde367c16421dc91c943ed3444d95c92aed41b0))

## [1.2.1](https://github.com/paulmeier/kasas/compare/v1.2.0...v1.2.1) (2026-06-05)


### Bug Fixes

* **dashboard:** account filter reset wrongly shows "No transactions" ([9018d5c](https://github.com/paulmeier/kasas/commit/9018d5c00fddd9b12a567d8f28a060ef4a1701dd))
* **dashboard:** account filter reset wrongly shows "No transactions" ([56437e4](https://github.com/paulmeier/kasas/commit/56437e414fed322ec4489971c790a9ce72a71e5b))

## [1.2.0](https://github.com/paulmeier/kasas/compare/v1.1.0...v1.2.0) (2026-06-05)


### Features

* add binary self-update (CLI, daily check, and dashboard banner) ([2be4cdf](https://github.com/paulmeier/kasas/commit/2be4cdfb083035c7cb1f51860e7ea9c92598f091))
* binary self-update (CLI + dashboard banner) ([9c2c00a](https://github.com/paulmeier/kasas/commit/9c2c00a9e13ca0756aa277310987acd7bdc55231))

## [1.1.0](https://github.com/paulmeier/kasas/compare/v1.0.0...v1.1.0) (2026-06-05)


### Features

* add read-only transactions dashboard (go-app WASM) ([65290d6](https://github.com/paulmeier/kasas/commit/65290d66012aaa0119cb19819c9cf38a0268a634))
* add read-only transactions dashboard (go-app WASM) ([e6e7ada](https://github.com/paulmeier/kasas/commit/e6e7ada6d2302fb257212f417b6f7585a31123ac))

## 1.0.0 (2026-06-05)


### Features

* SimpleFIN sync service (SQLite/Postgres), REST + MCP API, and CI/CD ([#1](https://github.com/paulmeier/kasas/issues/1)) ([f7bf6d7](https://github.com/paulmeier/kasas/commit/f7bf6d7738e163927c7c2cf732b52921fd0e9776))
