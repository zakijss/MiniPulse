# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- A finished test says how its download compares with your usual: the median of your earlier tests, once there are three.
- History: A shows a result on the main screen with its graphs, and START runs a new test.
- History: L2 and R2 jump to the newest and oldest result.

### Changed

- After an error, the footer reads A Retry and the error says to press A.

## [1.0.2] - 2026-11-08

### Changed

- A timeout says to try again or pick another server in Settings.
- A certificate error says to check the Miyoo's clock.
- A failed save says to check that the card has free space.
- The empty history says that tests you run with A show up there.
- Grey and red small text is easier to read (MiniDeck 1.0.3).

## [1.0.1] - 2026-11-02

### Fixed

- Deleting a result in History (Y) now asks first, like Clear all. One stray press used to remove it with no way back.
- The on-screen keyboard (server address) now shows how to leave it: B on an empty field goes back, and the footer shows B Back, or MENU Cancel once there is text (MiniDeck 1.0.1).

## [1.0.0] - 2026-11-02

### Added

- The first release: Cloudflare, LibreSpeed and OpenSpeedTest servers, a history, and settings for the test length, the number of connections and Mbit/s or MB/s.

[Unreleased]: https://github.com/zakijss/MiniPulse/compare/v1.0.2...HEAD
[1.0.2]: https://github.com/zakijss/MiniPulse/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/zakijss/MiniPulse/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/zakijss/MiniPulse/releases/tag/v1.0.0
