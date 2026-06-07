# Changelog

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
