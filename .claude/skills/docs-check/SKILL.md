---
name: docs-check
description: Finds what a change to openlog left undocumented and writes it. Use at the end of every piece of work that touched code, config, schema, an API or an SDK, before calling it done or committing. Also when asked to check or complete the documentation.
---

# Documentation check

openlog is meant to be used by people who did not write it, so something undocumented is unfinished. This runs at the
end of a piece of work and ends with the gaps written, not with a list of them.

## 1. See what changed

```sh
git status --short
git diff --stat @{u}
```

Read the diff itself, and list each thing a reader could now do, see or configure, or can no longer. A refactor with no such
thing needs no documentation: say so and stop.

## 2. Find where each one belongs

"Every language" below means every language in the table in step 3, and a page is not done until each of them has it.

| What changed | What has to say so |
|---|---|
| A new or changed environment variable | `docs/contracts/config.md`, `deploy/compose/.env.example`, `deploy/compose/docker-compose.yml`, `deploy/helm/openlog/values.yaml` |
| An endpoint or a field in a response | `docs/contracts/openapi.yaml`, then `npm run gen:api` in `web` and `dart run tool/gen_api.dart` in `mobile`; `docs/contracts/api.md` |
| A new field a console reads | Optional in `openapi.yaml`: the store console talks to older servers |
| Behaviour of a feature with a contract | That contract in `docs/contracts/`, including its status line and any "not built yet" that is now false |
| A design decision, or a choice against what a contract proposed | A new row in `docs/plan/02-decisions.md` (Turkish, `D-NNN`) and the contract's reasoning |
| What a user sets up or sees | The page under `docs/`, in every site language |
| An SDK option, capability or limit | That SDK's `agents/<name>/README.md`, which `docs/integrations/` includes, its written-out page under `docs/tr/integrations/`, and its row in the guide. If the same thing applies to other SDKs, each of them too: `ls agents` |
| A new SDK, page or contract | The sidebar in `docs/.vitepress/config.ts`, in every site language |
| A screen in the console | Every catalog in `web/src/i18n/locales/` |
| A new table | `internal/migrate/ttl.go` and the storage section of its contract |
| A new command in `cmd/` | How to get and run it, in the guide that needs it |

Then search for what the change made **false**, which is the gap nothing above catches:

```sh
git grep -n -i -E "not (yet|built|supported|available)|henüz|not reported|no longer|second step" -- docs agents/*/README.md
```

Also grep for the name of whatever changed. A sentence saying a thing does not exist is worse than silence.

## 3. Write it

- **Audience.** Guides are for someone integrating openlog: what to do, with commands that work. Contracts are for someone changing
  openlog: what it does and why.
- **Languages.** Complete in each one, never English now and the rest later:

  | Where | Languages | Source of truth |
  |---|---|---|
  | The site, except `docs/contracts/` and `docs/plan/` | English (default), Turkish | `locales` in `docs/.vitepress/config.ts` |
  | The console | English, Turkish | `web/src/i18n/locales/` |
  | Contracts, READMEs, code comments | English | |
  | `docs/plan/02-decisions.md`, commit messages | Turkish | |

  A translation says the same things with the same commands, in natural language rather than word for word, and keeps product terms such as `crash` as
  the existing pages do. A language added to the source of truth is held to all of this with no change here.
- **Examples** use `https://apm.resoft.org`, never a placeholder domain.
- **Say only what is true.** Run a command before documenting it. State how far something was tested when that is short of a real
  device, a real install or a real load, and state what is not covered. Do not write that something works because it should.

## 4. Check it

```sh
python3 .claude/skills/docs-check/check-locales.py
go test ./internal/config ./internal/migrate ./internal/api/...
(cd web && npm run typecheck)
(cd docs && npm run build)
```

`check-locales.py` fails on a page missing from a language, and on a translation whose headings or code blocks don't match the English. The typecheck
fails on a console string missing from a language. `TestEnvVarCoverage` fails on an undocumented variable. A dead link fails the docs build. None of them looks at whether the prose is right, so they
end this check and do not replace it.

Everything this change touched has to pass. Pages the script reports that this change did not touch are older gaps: fix them when asked to complete the
documentation, and otherwise give their count in the report, so they stay visible.

## 5. Report

One short paragraph to the user, in their language: what you added, and anything you left out and why. If nothing was missing, say so in
one line.
