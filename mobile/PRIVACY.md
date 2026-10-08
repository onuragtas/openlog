# Privacy statement — openlog mobile console

**Draft.** Written from what the app does, not from a template; the owner has to
approve it before it goes on a store listing. Every claim below is checkable
against this repository, and the places to check are named.

Last reviewed against the code: 2026-10-09.

## Who the app talks to

Exactly one server: the openlog installation whose address the person types on
the first screen. There is no openlog-operated backend, no analytics endpoint
and no crash reporter. The app has one networking file,
[`lib/src/api/client.dart`](lib/src/api/client.dart), and `grep` for `http.` in
`lib/` finds nothing outside it.

The runtime dependencies are `http`, `flutter_secure_storage`, `intl` and
`cupertino_icons` (see [`pubspec.yaml`](pubspec.yaml)). None of them report
anywhere.

## What is stored on the device

Three values, in the iOS Keychain and Android EncryptedSharedPreferences
([`lib/src/storage/token_store.dart`](lib/src/storage/token_store.dart)):

- the address of the installation,
- a device session token (`olm_…`), which the server issues at sign-in and can
  revoke from the web at any time,
- the id of the organization the person last acted in.

Signing out deletes all three.

Nothing else is written to the device. In particular the query console keeps
its recent queries in memory only: a query can carry a customer name or a host
name, and that is not this app's to persist
([`lib/src/query.dart`](lib/src/query.dart)).

## What is sent

Only what the person's own actions ask for: the requests listed in
[`lib/src/api/client.dart`](lib/src/api/client.dart), each carrying the device
token and the organization header. No device identifier, no advertising id, no
location, no contacts. The app requests no permissions on either platform —
beyond `INTERNET` on Android, which is what makes the requests above possible
([`android/app/src/main/AndroidManifest.xml`](android/app/src/main/AndroidManifest.xml)).

## What the installation sees

The server sees what any openlog client's requests show it: the account that
signed in, the device name entered at sign-in, the session's IP address and the
endpoints called. That is the installation's own log, under the control of
whoever runs it — not of the app's publisher. openlog's own data handling is
described in `docs/` at the root of this repository.

## Children

The app is a console for operating software. It is not directed at children and
collects nothing that would identify one.

## Changes

This file is versioned with the app. A change to what is stored or sent has to
change this file in the same commit; the reviewer's job is to check that it did.
