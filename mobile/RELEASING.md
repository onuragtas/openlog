# Releasing the mobile console

What is already decided and in the repository, and what still needs the owner.
Nothing here is a store submission; it is the groundwork for one.

## Decided, and where it lives

| Thing | Value | Where |
| --- | --- | --- |
| iOS bundle id | `org.resoft.openlogMobile` | `ios/Runner.xcodeproj/project.pbxproj` |
| Android application id | `org.resoft.openlog_mobile` | `android/app/build.gradle.kts` |
| Display name | Openlog Mobile | `ios/Runner/Info.plist`, `android/app/src/main/AndroidManifest.xml` |
| Marketing version | `pubspec.yaml` `version:` before the `+` | `pubspec.yaml` |
| Build number | the CI run number, passed as `--build-number` | not in `pubspec.yaml` |
| Privacy statement | [`PRIVACY.md`](PRIVACY.md) | versioned with the app |

The build number is deliberately not the `+1` in `pubspec.yaml`. A store needs
it to increase on every upload and a hand-edited number is one rebase away from
going backwards, so a store build passes
`flutter build ipa --build-number="$GITHUB_RUN_NUMBER"` and the file keeps only
the marketing version.

## What CI checks today

`ci.yml` runs three mobile jobs, all gated on `mobile/`,
`docs/contracts/openapi.yaml` or the workflow itself having changed:

- **mobile** — the generator guard (`gen_api --check`), the translation guard,
  `dart analyze --fatal-infos`, formatting and `flutter test`.
- **mobile-android** — a release APK, and a check that it actually has the
  `INTERNET` permission. That check exists because the app shipped without it:
  `flutter test` runs on the host and proves nothing about a packaged app.
- **mobile-ios** — a release build with `--no-codesign`. The signing identity is
  the owner's and is not in CI; what this catches is the pods, the deployment
  target and the native assets, which is what has actually broken.

## What the owner has to decide

These cannot be inferred from the code, and each one blocks a submission:

1. **Store accounts.** Apple Developer Program membership (the app is currently
   signed with a personal development certificate, team `WL2T3S849C`) and a
   Google Play developer account.
2. **Who the app is for.** A public listing, or TestFlight and Play internal
   testing for the organization's own on-call people? That changes the review
   burden and the screenshots entirely, and openlog is self-hosted: the app is
   useless to anyone without a server.
3. **Signing in CI.** Shipping from CI needs the distribution certificate and
   provisioning profile (iOS) and an upload keystore (Android) as secrets.
   `android/app/build.gradle.kts` still signs release with the debug key —
   fine for `flutter run --release`, not for a store.
4. **Review contact and a demo server.** App review needs an account to sign in
   with. A self-hosted console with no server to point at gets rejected.
5. **The privacy statement.** [`PRIVACY.md`](PRIVACY.md) is a draft written from
   the code. The claims in it are checkable; the wording and the legal footing
   are the owner's.
6. **Push notifications.** Still the open question from phase 6: the relay, an
   APNs key and a Firebase project.
