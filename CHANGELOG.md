# Changelog

## [1.3.1](https://github.com/schubydoo/balenamcp/compare/v1.3.0...v1.3.1) (2026-09-03)


### Bug Fixes

* **deps:** update module github.com/mark3labs/mcp-go to v1 ([#96](https://github.com/schubydoo/balenamcp/issues/96)) ([91110a5](https://github.com/schubydoo/balenamcp/commit/91110a51f497edc07afb072593e280d7518beb52))

## [1.3.0](https://github.com/schubydoo/balenamcp/compare/v1.2.2...v1.3.0) (2026-08-22)


### Features

* wrap device identify, rename, note and public-url ([#78](https://github.com/schubydoo/balenamcp/issues/78)) ([aa00a2a](https://github.com/schubydoo/balenamcp/commit/aa00a2a4a4830bc0b6c24cbc98d5ad06d4ddb840)), closes [#55](https://github.com/schubydoo/balenamcp/issues/55)
* wrap device os-update ([#79](https://github.com/schubydoo/balenamcp/issues/79)) ([a0cbdda](https://github.com/schubydoo/balenamcp/commit/a0cbddad4d808f1612ebdbc28203bb18b3f5e01e)), closes [#52](https://github.com/schubydoo/balenamcp/issues/52)
* wrap device rm, deactivate, move and register ([#80](https://github.com/schubydoo/balenamcp/issues/80)) ([9835011](https://github.com/schubydoo/balenamcp/commit/983501175b2f5cf05b6a96954b3d64cda7ed4020))
* wrap device start-service and stop-service ([#76](https://github.com/schubydoo/balenamcp/issues/76)) ([dac82d0](https://github.com/schubydoo/balenamcp/commit/dac82d0c575836cba405b37a887d124cf550a9e7)), closes [#54](https://github.com/schubydoo/balenamcp/issues/54)
* wrap fleet create and fleet rename ([#75](https://github.com/schubydoo/balenamcp/issues/75)) ([420370d](https://github.com/schubydoo/balenamcp/commit/420370d448dd118e4e69b2614f50b14b40a0cc19)), closes [#53](https://github.com/schubydoo/balenamcp/issues/53)
* wrap release-asset download, upload and delete behind a path allow-list ([#81](https://github.com/schubydoo/balenamcp/issues/81)) ([2065af8](https://github.com/schubydoo/balenamcp/commit/2065af8f1e09a29bbb679e8e3dd068f1801f4606))
* wrap ssh-key info, add and rm ([#82](https://github.com/schubydoo/balenamcp/issues/82)) ([fb15eea](https://github.com/schubydoo/balenamcp/commit/fb15eea05f62faa6f8ce0868f6a4b4cf9005b0e8)), closes [#58](https://github.com/schubydoo/balenamcp/issues/58)


### Bug Fixes

* annotate device-identify read-only instead of adding a class for it ([#84](https://github.com/schubydoo/balenamcp/issues/84)) ([8080f5f](https://github.com/schubydoo/balenamcp/commit/8080f5f9aa55a77e1cfcf4de00efaff3b146b390))
* close the four CLI-interaction bugs found by the parity audit ([#86](https://github.com/schubydoo/balenamcp/issues/86)) ([63ae625](https://github.com/schubydoo/balenamcp/commit/63ae625d6dc059becbf3ac44b76cec50ec183916))
* constrain device-purge and device-restart to one device per call ([#77](https://github.com/schubydoo/balenamcp/issues/77)) ([a3ffd48](https://github.com/schubydoo/balenamcp/commit/a3ffd48962a2fb5f5c816ea99ac1b67a6d3f12c8)), closes [#54](https://github.com/schubydoo/balenamcp/issues/54)
* **deps:** update module github.com/stretchr/testify to v1.12.1 ([#71](https://github.com/schubydoo/balenamcp/issues/71)) ([21d0db7](https://github.com/schubydoo/balenamcp/commit/21d0db76a5c72a6c2c2af45b8005a29a3840307e))
* stop annotating device-identify as destructive ([#83](https://github.com/schubydoo/balenamcp/issues/83)) ([a7cd969](https://github.com/schubydoo/balenamcp/commit/a7cd9695816cf96f98c0b64509e2422cf424cab7))

## [1.2.2](https://github.com/schubydoo/balenamcp/compare/v1.2.1...v1.2.2) (2026-08-18)


### Bug Fixes

* **deps:** update gomod (non-major) ([#64](https://github.com/schubydoo/balenamcp/issues/64)) ([208f509](https://github.com/schubydoo/balenamcp/commit/208f5098ee6682360fd2280f744f2e3f6b729805))
* **deps:** update module github.com/stretchr/testify to v1.12.0 ([#70](https://github.com/schubydoo/balenamcp/issues/70)) ([897d168](https://github.com/schubydoo/balenamcp/commit/897d1684b7ea006972ddf30fe63a5303dbf07f73))

## [1.2.1](https://github.com/schubydoo/balenamcp/compare/v1.2.0...v1.2.1) (2026-07-23)


### Bug Fixes

* **deps:** update module github.com/mark3labs/mcp-go to v0.57.0 ([#47](https://github.com/schubydoo/balenamcp/issues/47)) ([d36ad56](https://github.com/schubydoo/balenamcp/commit/d36ad568eacd12736f87a41fb27ccff7f0200d71))

## [1.2.0](https://github.com/schubydoo/balenamcp/compare/v1.1.0...v1.2.0) (2026-06-14)


### Features

* add 15 balena tools (ssh, fleet/release/org lifecycle, diagnostics) + gotchas resource ([#30](https://github.com/schubydoo/balenamcp/issues/30)) ([6613581](https://github.com/schubydoo/balenamcp/commit/6613581db456dc13a450d02d0a5cbbde7bb2c802))

## [1.1.0](https://github.com/schubydoo/balenamcp/compare/v1.0.1...v1.1.0) (2026-05-29)


### Features

* add balena:// MCP resources ([#22](https://github.com/schubydoo/balenamcp/issues/22)) ([ab55db5](https://github.com/schubydoo/balenamcp/commit/ab55db53bf061187a04389f05c9fb1d08bc7c752))
* add compare-releases, replicate-config, bulk-tag prompts ([#23](https://github.com/schubydoo/balenamcp/issues/23)) ([0d6ffc5](https://github.com/schubydoo/balenamcp/commit/0d6ffc5467f62ec9eecf21784fc988efbabb60e1))
* add MCP workflow prompts ([#20](https://github.com/schubydoo/balenamcp/issues/20)) ([464242f](https://github.com/schubydoo/balenamcp/commit/464242fa6ab13c55a6809a6f870e366af00ff8eb))
* add release, os-versions, and account-keys resources ([#24](https://github.com/schubydoo/balenamcp/issues/24)) ([8d62ba4](https://github.com/schubydoo/balenamcp/commit/8d62ba4f94872492ecdd644a8a189637530fc9d5))

## [1.0.1](https://github.com/schubydoo/balenamcp/compare/v1.0.0...v1.0.1) (2026-05-28)


### Miscellaneous

* release v1.0.1 ([#18](https://github.com/schubydoo/balenamcp/issues/18)) ([f89d72d](https://github.com/schubydoo/balenamcp/commit/f89d72dc99f14dd6c12b047ede29d0927cc3c8c4))

## 1.0.0 (2026-05-28)


### Features

* **device-logs:** refuse tail:true, drop from schema ([4ea8c5c](https://github.com/schubydoo/balenamcp/commit/4ea8c5cd4a66376b899569ad2e4081174c6cb90b))
* **device-track-fleet:** inverse of device-pin ([3c59edf](https://github.com/schubydoo/balenamcp/commit/3c59edfbc2755414809caa1d4e395971fa418522))
* optional confirm gate for destructive tools ([7f80c5e](https://github.com/schubydoo/balenamcp/commit/7f80c5ed37612af4268f5990c7853e94a840b9c9))
* per-call exec timeout and context propagation ([a0dfa99](https://github.com/schubydoo/balenamcp/commit/a0dfa99d47725ee04b9b0f2701b5312d230eb0ef))
* update for latest balena-cli and modernize MCP server ([03fc26c](https://github.com/schubydoo/balenamcp/commit/03fc26cfa6ef76849bb03eea4fb034d1001305cb))


### Bug Fixes

* **ci:** align settings.yml structure with working dump1090-exporter ([57e327c](https://github.com/schubydoo/balenamcp/commit/57e327c6a0d53fdeb30df677dbcb5f2abf5dcc27))
* **ci:** drop ./... from gremlins invocation ([fa383db](https://github.com/schubydoo/balenamcp/commit/fa383dbc2f362f774c2cbc139ba34e4bbc31e880))
* **ci:** pass --coverpkg=./... to gremlins ([0994818](https://github.com/schubydoo/balenamcp/commit/09948187174cd9d6a1b1c88fc9806d99de6908ea))
* **ci:** reorder imports in main.go to satisfy gofmt; pin LF endings ([6a7c204](https://github.com/schubydoo/balenamcp/commit/6a7c2043bcb934d81b76352f5903e413a332f914))
* **ci:** unstick Probot Settings — null vs false, topics whitespace ([a24d8d0](https://github.com/schubydoo/balenamcp/commit/a24d8d003926b99332ccf0d7b58c09b763168c15))
* **env-list:** reject --config + --service combo at the server ([5537bce](https://github.com/schubydoo/balenamcp/commit/5537bce53287019732e741f085db8ef98ae8e065))
* **lint:** gofmt -w over test files ([10ec4ec](https://github.com/schubydoo/balenamcp/commit/10ec4ec90170a13d60baa18617e696cce60ca4e4))
* **tag-list:** empty list is not an error ([3c59edf](https://github.com/schubydoo/balenamcp/commit/3c59edfbc2755414809caa1d4e395971fa418522))


### Build System & Dependencies

* goreleaser release with SBOMs + cosign keyless signing ([#6](https://github.com/schubydoo/balenamcp/issues/6)) ([ba13a11](https://github.com/schubydoo/balenamcp/commit/ba13a110e73661228e92123ebdba30b8b54b2e64))
