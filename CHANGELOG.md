# Changelog

## [0.14.0](https://github.com/constructorfleet/emberdeck/compare/v0.13.0...v0.14.0) (2026-10-28)


### Features

* **driveradapter:** declare manual control capabilities ([#135](https://github.com/constructorfleet/emberdeck/issues/135)) ([30c9117](https://github.com/constructorfleet/emberdeck/commit/30c9117f81f92e80ffdb096965b80dfcbc2c2526))


### Bug Fixes

* enrich project and job list context ([#138](https://github.com/constructorfleet/emberdeck/issues/138)) ([1a78923](https://github.com/constructorfleet/emberdeck/commit/1a789236887c7f3b6a677d79f59d5674cf11167b))

## [0.13.0](https://github.com/constructorfleet/emberdeck/compare/v0.12.0...v0.13.0) (2026-10-28)


### Features

* add authenticated camera monitoring ([#125](https://github.com/constructorfleet/emberdeck/issues/125)) ([48b7139](https://github.com/constructorfleet/emberdeck/commit/48b7139d6b09af881d78bf6da528eaa9fae3fefb))
* define versioned driver adapter API prototype ([#99](https://github.com/constructorfleet/emberdeck/issues/99)) ([0972123](https://github.com/constructorfleet/emberdeck/commit/0972123a63dc8ac3ae63efc693c0421d9dd17946))
* route assigned jobs through registered adapters ([#134](https://github.com/constructorfleet/emberdeck/issues/134)) ([44d5b7c](https://github.com/constructorfleet/emberdeck/commit/44d5b7cf3b280e47119fb722c57b7f30a450630e))


### Bug Fixes

* keep rectangle preview labels readable ([#130](https://github.com/constructorfleet/emberdeck/issues/130)) ([39da5cd](https://github.com/constructorfleet/emberdeck/commit/39da5cdae6e004f569383d306b0d9675222a5f99)), closes [#28](https://github.com/constructorfleet/emberdeck/issues/28)
* persist pre-stream worker failures ([#132](https://github.com/constructorfleet/emberdeck/issues/132)) ([13a8748](https://github.com/constructorfleet/emberdeck/commit/13a8748c33d4dfb9a043e83639bc2b541f781659))

## [0.12.0](https://github.com/constructorfleet/emberdeck/compare/v0.11.0...v0.12.0) (2026-10-28)


### Features

* **worker:** poll active lease during controller streaming ([#126](https://github.com/constructorfleet/emberdeck/issues/126)) ([0d8a63a](https://github.com/constructorfleet/emberdeck/commit/0d8a63a50eca32dfb74c98e33631067f158ffab7))

## [0.11.0](https://github.com/constructorfleet/emberdeck/compare/v0.10.2...v0.11.0) (2026-10-28)


### Features

* add portable one-assignment worker cycle ([#123](https://github.com/constructorfleet/emberdeck/issues/123)) ([58c221a](https://github.com/constructorfleet/emberdeck/commit/58c221ab511d1df4c22bfce14458f6176f67135a))

## [0.10.2](https://github.com/constructorfleet/emberdeck/compare/v0.10.1...v0.10.2) (2026-10-28)


### Bug Fixes

* discard stale MCU staging on oversized jobs ([#97](https://github.com/constructorfleet/emberdeck/issues/97)) ([2187c4e](https://github.com/constructorfleet/emberdeck/commit/2187c4eb8d7b0cdd9ede6c1eb629dace6395bf31))
* **server:** enforce exact terminal receipt retries ([#115](https://github.com/constructorfleet/emberdeck/issues/115)) ([8bc550d](https://github.com/constructorfleet/emberdeck/commit/8bc550d588c2324d2e015adec9f09a7b7a7dafc6))

## [0.10.1](https://github.com/constructorfleet/emberdeck/compare/v0.10.0...v0.10.1) (2026-10-28)


### Bug Fixes

* stabilize release screenshot capture ([#116](https://github.com/constructorfleet/emberdeck/issues/116)) ([ed98d2e](https://github.com/constructorfleet/emberdeck/commit/ed98d2e20d89fd9c80853b56858a80916443537e))

## [0.10.0](https://github.com/constructorfleet/emberdeck/compare/v0.9.0...v0.10.0) (2026-10-28)


### Features

* import bounded monoline SVG text ([#112](https://github.com/constructorfleet/emberdeck/issues/112)) ([27ca326](https://github.com/constructorfleet/emberdeck/commit/27ca326fbb30ad4a071eee9732d905ba70379f0c))

## [0.9.0](https://github.com/constructorfleet/emberdeck/compare/v0.8.0...v0.9.0) (2026-10-28)


### Features

* support nested SVG group transforms ([#109](https://github.com/constructorfleet/emberdeck/issues/109)) ([355cde4](https://github.com/constructorfleet/emberdeck/commit/355cde4fcd463e2358f606af636ce7494e32797c))


### Bug Fixes

* improve responsive console layout ([#108](https://github.com/constructorfleet/emberdeck/issues/108)) ([2aec1f6](https://github.com/constructorfleet/emberdeck/commit/2aec1f649bf39ae0acfa63047bc47eb920899786)), closes [#32](https://github.com/constructorfleet/emberdeck/issues/32)

## [0.8.0](https://github.com/constructorfleet/emberdeck/compare/v0.7.0...v0.8.0) (2026-10-28)


### Features

* add dedicated designer page and mouse transforms ([#101](https://github.com/constructorfleet/emberdeck/issues/101)) ([f67400b](https://github.com/constructorfleet/emberdeck/commit/f67400b1a150df3dbb43306a50b6bd1341131bb9))
* support smooth SVG path curves ([#104](https://github.com/constructorfleet/emberdeck/issues/104)) ([64ed7f8](https://github.com/constructorfleet/emberdeck/commit/64ed7f8bba868ca18267047b869590d156c9219e))


### Bug Fixes

* **designer:** resize rotated shapes in local axes ([#103](https://github.com/constructorfleet/emberdeck/issues/103)) ([c1f72dc](https://github.com/constructorfleet/emberdeck/commit/c1f72dcc66420311b8579c5a48f9ac8cb6e0d324))

## [0.7.0](https://github.com/constructorfleet/emberdeck/compare/v0.6.0...v0.7.0) (2026-10-28)


### Features

* add editable vector object transforms ([#85](https://github.com/constructorfleet/emberdeck/issues/85)) ([9d9c6c7](https://github.com/constructorfleet/emberdeck/commit/9d9c6c74a883c2c4054823b17f031f9e439d13ca))
* add ESP32 job staging contract prototype ([#88](https://github.com/constructorfleet/emberdeck/issues/88)) ([bfd4ff9](https://github.com/constructorfleet/emberdeck/commit/bfd4ff91e4d91be9856a5fe3761f1e7fceb8e410))
* align selected vector shapes ([#92](https://github.com/constructorfleet/emberdeck/issues/92)) ([fbc3522](https://github.com/constructorfleet/emberdeck/commit/fbc3522466eab81a67c524a582e04eb36ea2ccc2))
* **designer:** add editable monoline text engraving ([#98](https://github.com/constructorfleet/emberdeck/issues/98)) ([8153339](https://github.com/constructorfleet/emberdeck/commit/81533394dc63d7e44077879194b542cee8e701c3))
* record durable job event history and audit log ([#77](https://github.com/constructorfleet/emberdeck/issues/77)) ([6805ba3](https://github.com/constructorfleet/emberdeck/commit/6805ba3fea2e2e2d149c5da0aa8ba07af4373e9b))


### Bug Fixes

* make docs navigation responsive and dismissible ([#89](https://github.com/constructorfleet/emberdeck/issues/89)) ([c550a88](https://github.com/constructorfleet/emberdeck/commit/c550a880fd327bb82bca5ae616256eb5a7572fe7)), closes [#35](https://github.com/constructorfleet/emberdeck/issues/35)
* unify console status notices ([#90](https://github.com/constructorfleet/emberdeck/issues/90)) ([5b44810](https://github.com/constructorfleet/emberdeck/commit/5b4481036db7bedfe2ff40993a5b581ad5f5e155)), closes [#23](https://github.com/constructorfleet/emberdeck/issues/23)

## [0.6.0](https://github.com/constructorfleet/emberdeck/compare/v0.5.0...v0.6.0) (2026-10-28)


### Features

* add structured server and driver logging ([#84](https://github.com/constructorfleet/emberdeck/issues/84)) ([b030255](https://github.com/constructorfleet/emberdeck/commit/b0302552a97f75bfbd550826f9def65919f03f5c))
* add verified chunked worker transfers ([#83](https://github.com/constructorfleet/emberdeck/issues/83)) ([ee3f4ce](https://github.com/constructorfleet/emberdeck/commit/ee3f4ce6de3fd24cb541ffc3ffba3f8c305db270))
* confirm job start and cancel per job and driver ([#74](https://github.com/constructorfleet/emberdeck/issues/74)) ([ab2037f](https://github.com/constructorfleet/emberdeck/commit/ab2037f0e4f751c36fdf5f9aa60172137c494b8e))
* import bounded SVG Bezier curves ([#73](https://github.com/constructorfleet/emberdeck/issues/73)) ([4c2e860](https://github.com/constructorfleet/emberdeck/commit/4c2e860b3f99a806bf8d99cb85f72cc0a3c8d1ce))
* verify controller laser settings before streaming ([#79](https://github.com/constructorfleet/emberdeck/issues/79)) ([e8a289a](https://github.com/constructorfleet/emberdeck/commit/e8a289a25446876e5b6e335cfbf1b8903faf75a8))


### Bug Fixes

* consolidate console tokens and meet contrast ([#80](https://github.com/constructorfleet/emberdeck/issues/80)) ([ce4b8cc](https://github.com/constructorfleet/emberdeck/commit/ce4b8cc0102bbc07fd571a42e40012d254c115b4)), closes [#31](https://github.com/constructorfleet/emberdeck/issues/31)

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
