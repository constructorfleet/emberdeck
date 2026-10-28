# Changelog

## [0.5.0](https://github.com/constructorfleet/emberdeck/compare/v0.4.0...v0.5.0) (2026-10-28)


### Features

* add multi-path vector designer workflow ([#34](https://github.com/constructorfleet/emberdeck/issues/34)) ([ebd8ccb](https://github.com/constructorfleet/emberdeck/commit/ebd8ccbb11ddcec2d27c6ae2a09f86b42a4f0347))
* add optional Prometheus metrics exporter ([#65](https://github.com/constructorfleet/emberdeck/issues/65)) ([d6476c6](https://github.com/constructorfleet/emberdeck/commit/d6476c6411ef24041e5077352eaf3fc665ba7f21)), closes [#21](https://github.com/constructorfleet/emberdeck/issues/21)
* add scoped operator and worker credentials ([#72](https://github.com/constructorfleet/emberdeck/issues/72)) ([1afae6f](https://github.com/constructorfleet/emberdeck/commit/1afae6fb4f49a5df62dfa14ae91d3088c6ebf595)), closes [#56](https://github.com/constructorfleet/emberdeck/issues/56)
* import static SVG geometry into vector projects ([#70](https://github.com/constructorfleet/emberdeck/issues/70)) ([aa3f4b7](https://github.com/constructorfleet/emberdeck/commit/aa3f4b7d9dc2fd768a941fc66bd4a503c6abe90f))
* review uploaded G-code against machine profiles ([#66](https://github.com/constructorfleet/emberdeck/issues/66)) ([0129903](https://github.com/constructorfleet/emberdeck/commit/0129903cdb43d0af288037a443452efba62a507f))
* show live toolpath progress in operator console ([#61](https://github.com/constructorfleet/emberdeck/issues/61)) ([7eb0681](https://github.com/constructorfleet/emberdeck/commit/7eb0681165ffcef3c7b045ab8ec260b7688defd7))


### Bug Fixes

* extend ack deadline while controller reports Run progress ([#63](https://github.com/constructorfleet/emberdeck/issues/63)) ([208d70e](https://github.com/constructorfleet/emberdeck/commit/208d70eb4623d7fc72fceec8df62132c5e33984a))
* keep idle workers and all-in-one server alive across HTTP errors ([#62](https://github.com/constructorfleet/emberdeck/issues/62)) ([d4149b5](https://github.com/constructorfleet/emberdeck/commit/d4149b5ef7b9773a343434dc374d474d9ab86551))
* make serial modem policy explicit and refuse reset banners ([#69](https://github.com/constructorfleet/emberdeck/issues/69)) ([1e6d13a](https://github.com/constructorfleet/emberdeck/commit/1e6d13a2ad2db2dfaa4af6d4e6f0acb92d5f7c4e)), closes [#48](https://github.com/constructorfleet/emberdeck/issues/48)
* manage API key session lifecycle ([#64](https://github.com/constructorfleet/emberdeck/issues/64)) ([d1f3dc0](https://github.com/constructorfleet/emberdeck/commit/d1f3dc0e550b190ab2768b7b1bf1e72e6632344d)), closes [#26](https://github.com/constructorfleet/emberdeck/issues/26)
* serve favicon and return 404 for unknown pages ([#47](https://github.com/constructorfleet/emberdeck/issues/47)) ([5f9c25f](https://github.com/constructorfleet/emberdeck/commit/5f9c25f9ea5e2749522e5e4e58a9dda39138281d)), closes [#33](https://github.com/constructorfleet/emberdeck/issues/33)
* split job programs from metadata and paginate job listing ([#67](https://github.com/constructorfleet/emberdeck/issues/67)) ([92bc7dd](https://github.com/constructorfleet/emberdeck/commit/92bc7ddf2004b1cf521c1f939ae9571d21d2e63d))

## [0.4.0](https://github.com/constructorfleet/emberdeck/compare/v0.3.0...v0.4.0) (2026-10-28)


### Features

* add saved profiles and rectangle engraving workflow ([#17](https://github.com/constructorfleet/emberdeck/issues/17)) ([9637f63](https://github.com/constructorfleet/emberdeck/commit/9637f63967bccf317912bc6ad34c817db8f0313a))

## [0.3.0](https://github.com/constructorfleet/emberdeck/compare/v0.2.2...v0.3.0) (2026-10-28)


### Features

* add categorized documentation navigation ([#13](https://github.com/constructorfleet/emberdeck/issues/13)) ([8e6df8e](https://github.com/constructorfleet/emberdeck/commit/8e6df8ee7e8358d7d60f9abd1dd8b93fc7011fde))

## [0.2.2](https://github.com/constructorfleet/emberdeck/compare/v0.2.1...v0.2.2) (2026-10-28)


### Bug Fixes

* cancel pending resume when a new pause is applied ([#11](https://github.com/constructorfleet/emberdeck/issues/11)) ([3707035](https://github.com/constructorfleet/emberdeck/commit/37070357216df67b8c1874bc3612f6e65640fb73))

## [0.2.1](https://github.com/constructorfleet/emberdeck/compare/v0.2.0...v0.2.1) (2026-10-28)


### Bug Fixes

* preserve pause and resume through final controller drain ([#9](https://github.com/constructorfleet/emberdeck/issues/9)) ([a8ee078](https://github.com/constructorfleet/emberdeck/commit/a8ee0784807d0372208af60bef53adc854c1e711))

## [0.2.0](https://github.com/constructorfleet/emberdeck/compare/v0.1.0...v0.2.0) (2026-10-28)


### Features

* add confirmed job control and illustrated operator guides ([#5](https://github.com/constructorfleet/emberdeck/issues/5)) ([b2ce26f](https://github.com/constructorfleet/emberdeck/commit/b2ce26fad561ac2f8570fd68fbc44f1feb888e37))

## 0.1.0 (2026-10-28)


### Features

* launch EmberDeck job management and distributed GRBL foundation ([#1](https://github.com/constructorfleet/emberdeck/issues/1)) ([ca1a5a5](https://github.com/constructorfleet/emberdeck/commit/ca1a5a5fe2d311d90a436d806b3da494d5675ec6))
