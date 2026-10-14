# Install a release

The latest CLI beta is [v0.1.0-beta.4](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.4),
pinned to protected main commit `ca3d5889a52815bce9565d3825b081839ac7077b`.
It includes owner-only Personal projects, native Nx Local/direct private
endpoints, permanent-conflict pin retirement, complete Xcode graph retention,
and confirmed publication of successful local Turbo tasks for CI reuse. Required
checks can reuse matching tasks with `cache-mode: read-only`; a fresh task lane
uses `cache-mode: fresh`. Xcode graph retention passed real multiple-output
replay under quota pressure; Phone's larger build needs separate timing and
compatibility qualification. Nx Local-to-private forwarding remains future
work. Public Builds
are not qualified by this prerelease; a hosted self-serve cloud requires separate
deployment. Run the installer from an inspected checkout pinned to that source commit:

```sh
bash scripts/install.sh --repository ziahamza/layercache --version v0.1.0-beta.4 \
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

The [beta.4 release workflow](https://github.com/ziahamza/layercache/actions/runs/38552629557)
qualifies native release candidates before immutable publication. The
[published installer workflow](../.github/workflows/beta-release-install.yml)
pins beta.4 and its exact source commit. It verifies the locked tag, attestations,
checksum, installation into an empty prefix, `help`, and setup preview on Linux
x64, Linux arm64, and macOS arm64. Each job then runs the
[17-scenario real Turbo acceptance](../qa/local-ci-reuse/README.md) using the
installed release binary, followed by the pinned compiled Nx client acceptance.
Both use the published CLI; only Nx's QA credential helper is compiled. The retained JSON
contains task execution, restore, changed-input and offline-recovery evidence;
it contains no daemon configuration or credentials.

The installer and acceptance fixtures come from that same exact release source.
The Turbo acceptance uses production metadata deadlines and deterministic delayed
responses, and requires permanent conflict pins to retire. Nx acceptance proves
Local/restart replay, a fresh read-only direct consumer with zero task executions,
exact outputs/logs, changed-input/profile misses and failed-test reruns.

Historical beta.3 qualification used installer source
`d99e6f685b5db878b924181926399c57be9f16d7` and a separately reviewed QA checkout
at `8990f7dd8e662c3aeebbc4a0ff9bccc03cb1d820`. Its known retained conflict pins
were explicitly excluded from that historical run. Beta.4 has no such exception.
The [beta.3 release workflow](https://github.com/ziahamza/layercache/actions/runs/38478627110)
remains its publication record.

The earlier [beta.2 release workflow](https://github.com/ziahamza/layercache/actions/runs/38155658632)
passed native Linux amd64/arm64 and macOS arm64 qualification before immutable
publication. The
[published-binary installer workflow](https://github.com/ziahamza/layercache/actions/runs/38156102533)
then passed on fresh Linux x64, Linux arm64, and macOS arm64 runners. It pins
beta.2 and its exact source commit. Each job verified the immutable release,
tag source, attestations, archive checksum, `help`, and `setup --preview --json`
after installation into an empty prefix.
