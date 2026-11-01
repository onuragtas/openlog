---
name: parity-check
description: Finds what the web console and the mobile console of openlog don't both do, and builds what is missing. Use at the end of any work that touched web/src or mobile/lib, or added or changed an API endpoint a console reads, before calling it done or committing. Also when asked to compare or align the two consoles.
---

# Web and mobile parity

The web console (`web/`) and the mobile console (`mobile/`) are one product. A feature added to one is not finished until it is in the other, and both should reach the same answer the same way: the same filters, the same defaults, the same
sorting and paging, the same wording of a state, the same rules about what counts as what. This ends with the gap closed, not with a list of it.

## 1. Find the gap

```sh
python3 .claude/skills/parity-check/check-parity.py
```

It compares the API calls each console makes, which catches a screen one of them lacks and a list parameter one of them doesn't send. It can't see logic. So also read the change and ask, for each thing it does:

- Is the same **rule** applied on the other side: a threshold, a default range, what a blank or a zero means, when something reads "new" or "–"?
- Does the **logic** live in the server? Where it has to be computed in a client, prefer moving it to the server over writing it twice. If it stays, the two have to compute it the same way, and each side's test should use the same example values.
- Are the **strings** the same in meaning, in both languages of both consoles?

## 2. Close it

Build the missing side in that console's own idiom. Parity means the same abilities and the same answers, not the same layout: a table on the web is often a list on a phone, and a click on a heading is often a sort control. Read how the neighbouring screens do it first.

**Mobile** (`mobile/`)
- Calls go in `lib/src/api/client.dart`, a list's logic in a `lib/src/<area>.dart` controller, and its screen in `lib/src/ui/`. `lib/src/list_controller.dart` is what every list does when things go wrong.
- Strings go in both `lib/l10n/app_en.arb` and `app_tr.arb`.
- After an OpenAPI change: `dart run tool/gen_api.dart`. Use `PATH="$HOME/flutter/bin:$PATH"`, since the Dart on the default path may be too old.
- Check: `flutter analyze`, `flutter test`, `dart run tool/check_l10n.dart`, `dart run tool/gen_api.dart --check`.

**Web** (`web/`)
- Query factories go in `src/api/`, screens in `src/components/` and `src/routes/`. Lists use `SortableHead`, `ListPager` and `useListState`.
- Strings go in both `src/i18n/locales/en.ts` and `tr.ts`.
- After an OpenAPI change: `npm run gen:api`.
- Check: `npm run typecheck`, `npx vitest run`, `npx eslint src`.

**The store's mobile app updates more slowly than a server, and the other way around.** A new field the consoles read must be optional in `docs/contracts/openapi.yaml`, and a console must keep working when an older server doesn't send it. A new mobile call needs to handle the `404` of a server that doesn't have it yet.

## 3. Exceptions

A real exception goes in `.claude/skills/parity-check/parity-exceptions.txt`, one per line with its reason: `GET /api/v1/x  # why`. For list parameters, `params GET /api/v1/x  # why`. A gap only counts as an exception when the platform makes it one, such as the way each console signs in. "Not done yet" and "nobody uses it on a phone" are not reasons: ask the user, because that is their decision to make. When the user decides a whole area stays on one side, record it there with their reason.

## 4. Check it

Run step 1 again, then the checks of both consoles. See a new screen working where you can: a test proves the logic and not that the layout fits a phone. Then run `docs-check`, since a new ability usually needs documenting.

## 5. Report

One short paragraph to the user, in their language: what you built on which side and what you could not run. If the script reports gaps this change did not cause, give their count, so they stay visible. Don't close an older gap unasked if it is large: say how large and ask.
