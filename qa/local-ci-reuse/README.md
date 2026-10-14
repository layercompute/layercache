# Local results available to CI acceptance

This command runs a real Turbo 2.10.12 client against an installed LayerCache
candidate. It creates loopback Team and Local Cache daemons, disposable git
clones and an external execution counter. No production cache or CI dispatch is
used. The command stops its daemons and removes its temporary directory.

```bash
pnpm --dir qa/fixtures/local-ci-reuse install --frozen-lockfile
go build -o /tmp/layercache-local-ci-reuse ./cmd/layercache
python3 qa/local-ci-reuse/run.py \
  --binary /tmp/layercache-local-ci-reuse \
  --turbo "$PWD/qa/fixtures/local-ci-reuse/node_modules/.bin/turbo" \
  --output /tmp/layercache-local-ci-reuse.json
```

Successful builds and tests publish with `run --sync-team --sync-timeout 10s`.
The cold publication and recovered fresh restore include 400 ms metadata delays
under the production two-second metadata budget. Offline confirmation retains a
one-second deadline. This acceptance checks reuse correctness; calibrated timing
tests remain separate.
Warm repetition and a fresh read-only consumer must restore byte-identical
outputs without executing tasks. Failed tests retain exit code 7 and run again.
Changing source, a declared dependency input, a declared environment variable
or the runtime's compatibility namespace must miss. Main's fresh-test profile
uses `--cache=local:,remote:` and must execute without storing task
artifacts.

Turbo 2.10.12 rejects combining `--force` and `--cache`. Empty read/write modes
already disable task-result caching and force actual execution.

The command also checks same-hash archives with different package-manager logs,
confirmation while unrelated uploads remain pending, offline local reuse and
durable queued publication after a daemon restart. Readiness must appear only
after Team confirms the completed tasks. JSON evidence contains native task
hashes, real Turbo summaries, execution counts, output digests, elapsed times
and publication warnings. It never contains daemon configuration or tokens.

Current candidates must retire verified immutable conflicts. When qualifying
the immutable beta.3 release only, pass `--allow-legacy-conflict-pins` to preserve
its known [#24](https://github.com/ziahamza/layercache/issues/24) limitation.
This exception does not weaken fresh restore, failed-test, input-change,
offline-recovery or archive-digest assertions.

The small fixture is an acceptance test, not a Phone performance benchmark.
`--keep-workspace` retains its isolated installation for diagnosis. Treat the
retained configuration files as temporary credentials.
