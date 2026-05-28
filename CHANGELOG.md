# Changelog

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
