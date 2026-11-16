# Working on agentgateway-presidio

`presidio-guard` is an agentgateway guardrail webhook in one Go package: `webhook.go` takes the
request or response from agentgateway, `presidio.go` asks the Presidio analyzer, `mask.go` replaces
the findings, and `cache.go` keeps the findings of messages already seen in a conversation.
`deploy/` holds the manifests. The README describes the behaviour; keep it in step with any change a user would notice. `CHANGELOG.md` is written by
release-please from the commit messages: never edit it by hand, write the commit message instead.

## Commit messages

Every commit follows [Conventional Commits](https://www.conventionalcommits.org/): a subject line
`type(scope)!: summary`, a blank line, a body. The scope is optional. The `commits` workflow checks
every commit pushed to `main` or in a pull request, and the pull request title, which becomes the
commit of a squash merge.

| Type | Use for | In the changelog | Next version (before 1.0) |
| --- | --- | --- | --- |
| `feat` | Something a user can now do or configure | Features | minor |
| `fix` | A user-visible bug fixed, dependency updates | Bug Fixes | patch |
| `perf` | Faster or smaller, same behaviour | Performance Improvements | patch |
| `refactor`, `test`, `docs`, `build`, `ci`, `chore`, `style` | Everything else | not listed | none |

A `!` after the type, or a `BREAKING CHANGE:` footer, marks a breaking change; before 1.0 it bumps
the minor version. The `feat` and `fix` subjects are the release notes, so write them for someone
who runs the guard: "fix: mask a finding that spans two messages", not "fix: offset bug in mask.go".
One change per commit.

## Checks

```sh
gofmt -l .         # must print nothing
go vet ./...
go test ./...
docker build .     # what the image job in ci builds, for linux/amd64 and linux/arm64
```

Run the first three before every commit.

## Rules

- Test data is invented. Never commit real PII, or prompts and responses captured from a real
  gateway.
- No local paths, cluster names, account IDs or internal tool names in committed files.
- Never edit the version in `deploy/presidio-guard.yaml` by hand: release-please updates the line
  marked `x-release-please-version`.
- Commit locally on the current branch. Do not push, tag, open or merge pull requests (the release
  pull request included) unless the owner asked for it.

## Dependency updates

Renovate opens them weekly (`renovate.json`) with Conventional Commit titles (`fix(deps):` for Go
modules, `chore(deps):` for GitHub Actions and the rest). A pull request that
passes `ci` and `commits` is fine to merge.

## Releasing

Releases are cut by release-please (`release-please-config.json`, `.release-please-manifest.json`).
On every push to `main` it opens or updates a pull request titled `chore(main): release X.Y.Z`,
which sets the version in the manifest and adds that version's section to `CHANGELOG.md`.
The `feat`, `fix` and `perf` commits since the last release decide the version and the text.

Merging that pull request is the release, so only the owner merges it. release-please then tags
`vX.Y.Z` and publishes the GitHub release with that section as notes, and calls the `release`
workflow. That workflow tests, builds the image for linux/amd64 and linux/arm64, pushes it to
`ghcr.io/paurosello/presidio-guard:vX.Y.Z`, and adds the image reference with its digest to the
release notes.

- Never edit `CHANGELOG.md` or `.release-please-manifest.json` by hand, and never push a release
  tag yourself. To change what a release says, change the release pull request or the commit
  messages, not the files.
- The release pull request is opened with the workflow token, so `ci` and `commits` do not run on
  it; it only changes the changelog and the version manifest.
- A failed `release` job can be re-run from the Actions tab; every step repeats safely. To run the
  current workflow for a tag, use "Run workflow" on `release` with that tag.
