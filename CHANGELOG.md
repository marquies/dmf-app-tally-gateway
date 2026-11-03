# Changelog

## [0.2.0](https://github.com/marquies/dmf-app-tally-gateway/compare/v0.1.0...v0.2.0) (2026-11-03)


### Features

* **gateway:** sign people in on the page and API through OIDC ([#2](https://github.com/marquies/dmf-app-tally-gateway/issues/2)) ([fce4302](https://github.com/marquies/dmf-app-tally-gateway/commit/fce430230c35619bb0d322dd6f163224b3be93fd))

## 0.1.0 (2026-11-01)


### Features

* **chart:** add the tally-gateway Helm chart ([0db789d](https://github.com/marquies/dmf-app-tally-gateway/commit/0db789d54e21879338a8695b53899aece95d24fc))
* **gateway:** close device connections on shutdown ([1662e9c](https://github.com/marquies/dmf-app-tally-gateway/commit/1662e9cf8a010de0108ec444e1547d76abf9ad35))
* **gateway:** serve tally to devices over WebSocket ([fb1ccf9](https://github.com/marquies/dmf-app-tally-gateway/commit/fb1ccf93a6f35ec97b97f239ece3ecdb1204cb6a))
* **registry:** persist device source assignments ([e969b3a](https://github.com/marquies/dmf-app-tally-gateway/commit/e969b3af43f2a0d6af0e41d6091d843caf574a63))
* **upstream:** poll SwitchFrame for tally state ([734f7a9](https://github.com/marquies/dmf-app-tally-gateway/commit/734f7a92c21e9922d372e02f240212d67fd1a729))


### Build System

* add the container image ([a6ebc64](https://github.com/marquies/dmf-app-tally-gateway/commit/a6ebc648682710753f04cb2179edaee3f10ab613))
* pin the Go toolchain with mise ([8e3bcc7](https://github.com/marquies/dmf-app-tally-gateway/commit/8e3bcc7c07d46dcb3c12182980285426876fa7ea))


### Continuous Integration

* open release pull requests with the release-please token ([d20fbf6](https://github.com/marquies/dmf-app-tally-gateway/commit/d20fbf6f5a7e4bb0b2bc1b25c7ca76630bd290f5))
* test, build and release the gateway and its chart ([250788c](https://github.com/marquies/dmf-app-tally-gateway/commit/250788c1b074a5fd562da59683c78518aad26d95))
