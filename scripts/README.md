# Install a release

The latest CLI beta is [v0.1.0-beta.10](https://github.com/layercompute/layercache/releases/tag/v0.1.0-beta.10),
pinned to protected main commit `9e29b2695e6816d95504f2f7bb0b91014e50d5dc`.
It preserves project identities after repository transfers and supports project-bound
HTTPS GitHub OIDC audiences. The release source also includes the corrected
Native action bundle.
It retains immutable operation reports and explicit complete-interval comparisons.
The separate [native recorder](../docs/operation-measurement.md) runs offline;
the Go CLI imports its sealed report. Native Gradle/Xcode reporting, offline
report exporters and dashboard coverage explanations remain available.
Original producer timing requires matched
native keys, immutable bytes and execution windows. Native task elapsed time,
whole-job allocation, CPU use and billing remain separate measurements. See the
[native metering contract](../docs/native-metering.md). It retains native Bazel
HTTP integration, local storage inspection, hosted storage plans, standalone
Xcode cache lifecycle, native Gradle HTTP caching, complete-stream publication guards,
owner-only Personal projects, native Nx Local/direct private endpoints,
permanent-conflict pin retirement, complete Xcode graph retention and confirmed
publication of successful local Turbo tasks for CI reuse. Required
checks can reuse matching tasks with `cache-mode: read-only`; a fresh task lane
uses `cache-mode: fresh`. Xcode graph retention passed real multiple-output
replay under quota pressure; managed Xcode Cloud builds, external Swift replay
and application savings need separate qualification. Nx Local-to-private
forwarding remains future work. Public Builds are not qualified by this
prerelease; a hosted self-serve cloud requires separate deployment. Run the
installer from an inspected current checkout. The binary remains pinned to the
release source above:

```sh
bash scripts/install.sh --repository layercompute/layercache --version v0.1.0-beta.10 \
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

The repository moved to `layercompute/layercache`. Beta.9 retains its original
`ziahamza/layercache` signing identity. The current installer verifies preserved
public attestation bundles only for that exact release and source commit; all
other releases require attestations from the requested repository. It never
retries verification against a different signer. See the
[transfer provenance policy](provenance/README.md).

The signing policy uses the official
[GitHub attestation verifier](https://cli.github.com/manual/gh_attestation_verify).
An old `gh` without the required verification flags fails rather than weakening
the policy. The Node test uses a local fake GitHub client to exercise policy
failures and target preservation.

The [beta.10 release workflow](https://github.com/layercompute/layercache/actions/runs/40791848078)
qualified native release candidates before immutable publication. The
[published installer workflow](../.github/workflows/beta-release-install.yml)
retains beta.9 and its exact source as the historical PR regression fixture.
An [explicit beta.10 dispatch](https://github.com/layercompute/layercache/actions/runs/40792963587)
qualified the new release with its exact tag, source and native-bundle digest.
It checks the locked tag, attestations, checksum, binary source/platform
metadata, installation into an empty prefix,
`help`, and setup preview on Linux x64, Linux arm64, and macOS arm64. Each job runs
the [17-scenario real Turbo acceptance](../qa/local-ci-reuse/README.md) using the
installed release binary, followed by the pinned compiled Nx client acceptance.
Linux x64 also runs the 22-command pinned Gradle 9.3.1/JDK 17 acceptance and
binds its receipt to the installed binary, exact release source and init script.
Gradle acceptance on the other two architectures remains unqualified.
Each platform also runs the [24-control standalone lifecycle and scoped archive
qualification](../qa/xcode-lifecycle/README.md), binding the installed binary and
native client hashes. This uses synthetic CAS data, not Xcode compilation or an
Apple-managed build. The native archive client is a separately SHA-pinned commit
blob, not a release asset or an attested binary.
All cache traffic uses the published CLI; only acceptance harnesses and the QA
credential helper are compiled. The retained JSON contains task execution,
restore, changed-input and offline-recovery evidence; it contains no daemon
configuration or credentials.

The acceptance fixtures come from the exact release source. The installer and
its provenance policy come from the reviewed current checkout so that historical
releases can be verified after a repository transfer.
The Turbo acceptance uses production metadata deadlines and deterministic delayed
responses, and requires permanent conflict pins to retire. Nx acceptance proves
Local/restart replay, a fresh read-only direct consumer with zero task executions,
exact outputs/logs, changed-input/profile misses and failed-test reruns.

The [transfer qualification](../docs/evidence/repository-transfer-20261029.md)
records the separate source, release, installation and hosted-reuse outcomes.
The [beta.9 release workflow](https://github.com/ziahamza/layercache/actions/runs/39584178827)
remains its historical publication record with its original signing identity.

Historical [beta.5 installer qualification](https://github.com/ziahamza/layercache/actions/runs/38895127715)
passed all three platforms. Its [retained summary](../docs/evidence/beta5-installed-20261016.json)
records that older source and qualification scope.

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
