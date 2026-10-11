# Install a release

The latest CLI beta is [v0.1.0-beta.2](https://github.com/ziahamza/layercache/releases/tag/v0.1.0-beta.2), pinned to protected main commit `447095385671323a186299e0718d10c6459abb6f`. It includes the reviewed Team Cache authentication fixes. Public Builds are not qualified by this prerelease; a hosted self-serve cloud requires separate deployment. Run the installer from an inspected checkout pinned to that source commit:

```sh
bash scripts/install.sh --repository ziahamza/layercache --version v0.1.0-beta.2 \
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

The [beta.2 release workflow](https://github.com/ziahamza/layercache/actions/runs/38155658632)
qualifies native Linux amd64/arm64 and macOS arm64 candidates before publication.
The [published-binary installer workflow](../.github/workflows/beta-release-install.yml)
is pinned to beta.2 and its exact source commit. Fresh-runner verification for
this release is pending. Each job must check the immutable release, tag source,
attestations, archive checksum, `help`, and `setup --preview --json` on a fresh
Linux x64, Linux arm64, or macOS arm64 runner.
