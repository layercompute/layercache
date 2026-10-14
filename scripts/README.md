# Install a release

The latest CLI beta is [v0.1.0-beta.3](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.3),
pinned to protected main commit `d99e6f685b5db878b924181926399c57be9f16d7`.
It includes the reviewed Team Cache authentication fixes, confirmed publication
of successful local Turbo tasks for CI reuse, and the Xcode adapter. Required
checks can reuse matching tasks with `cache-mode: read-only`; a fresh task lane
uses `cache-mode: fresh`. Complete Xcode graph protection under eviction remains
tracked in [#25](https://github.com/ziahamza/layercache/issues/25). Public Builds
are not qualified by this prerelease; a hosted self-serve cloud requires separate
deployment. Run the installer from an inspected checkout pinned to that source commit:

```sh
bash scripts/install.sh --repository ziahamza/layercache --version v0.1.0-beta.3 \
  --prefix "$HOME/.local/bin"
"$HOME/.local/bin/layercache" help
```

The repository and version are explicit inputs. The source checkout supplies only
the installer script; binaries come from the published immutable release.
The installer supports Linux amd64/arm64 and macOS arm64, and requires
`gh`, `jq`, `tar`, and `shasum`. Authenticate `gh` or supply `GH_TOKEN` with read
access to the release and attestations.

Installation requires a published immutable GitHub release. It peels the locked
tag to a commit, downloads the platform archive and checksum, and verifies both
with GitHub's attestation verifier. Verification binds the publishing repository,
`release.yml`, tag, source commit, and GitHub hosted builder. It then checks the
archive checksum and permits exactly one regular binary member. Only a verified
binary that passes `help` replaces the installed executable, using an atomic
rename. Symlink targets are refused. There is no skip-verification option.

The signing policy uses the official
[GitHub attestation verifier](https://cli.github.com/manual/gh_attestation_verify).
An old `gh` without the required verification flags fails rather than weakening
the policy. The Node test uses a local fake GitHub client to exercise policy
failures and target preservation.

The [beta.3 release workflow](https://github.com/ziahamza/layercache/actions/runs/38478627110)
qualifies native release candidates before immutable publication. The
[published installer workflow](../.github/workflows/beta-release-install.yml)
pins beta.3 and its exact source commit. It verifies the locked tag, attestations,
checksum, installation into an empty prefix, `help`, and setup preview on Linux
x64, Linux arm64, and macOS arm64. Each job then runs the
[17-scenario real Turbo acceptance](../qa/local-ci-reuse/README.md) using the
installed release binary, with no candidate compilation. The retained JSON
contains task execution, restore, changed-input and offline-recovery evidence;
it contains no daemon configuration or credentials.

The installer still comes from the exact release source. Acceptance uses a
separate inspected QA checkout, pinned to
`8990f7dd8e662c3aeebbc4a0ff9bccc03cb1d820`, with the production metadata timeout
and deterministic delayed responses. Beta.3's known retained conflict pins are
explicitly excluded from its historical qualification; current candidates must
retire them. The binary, tag, attestation and checksum checks remain unchanged.

The earlier [beta.2 release workflow](https://github.com/ziahamza/layercache/actions/runs/38155658632)
passed native Linux amd64/arm64 and macOS arm64 qualification before immutable
publication. The
[published-binary installer workflow](https://github.com/ziahamza/layercache/actions/runs/38156102533)
then passed on fresh Linux x64, Linux arm64, and macOS arm64 runners. It pins
beta.2 and its exact source commit. Each job verified the immutable release,
tag source, attestations, archive checksum, `help`, and `setup --preview --json`
after installation into an empty prefix.
