#!/usr/bin/env bash
set -euo pipefail
binary="${1:?Layer Cache binary required}"
evidence="${2:?evidence directory required}"
pressure_proxy="${3:-}"
mkdir -p "$evidence/project" "$evidence/socket"
chmod 700 "$evidence/socket"
xcodebuild -version > "$evidence/toolchain.txt"
abi="xcode-probe-v1-$( { xcodebuild -version; xcrun clang --version; sw_vers -buildVersion; } | shasum -a 256 | cut -d ' ' -f 1)"
cat > "$evidence/project/main.m" <<'SOURCE'
@import Foundation;
extern int swift_probe_value(void);
int main(void) { @autoreleasepool { NSLog(@"Layer Cache compiler probe %d", swift_probe_value()); } return 0; }
SOURCE
cat > "$evidence/project/Probe.swift" <<'SWIFT'
@_cdecl("swift_probe_value")
func swiftProbeValue() -> Int32 { 42 }
SWIFT
ruby - "$evidence/project" <<'RUBY'
require 'xcodeproj'
root = ARGV.fetch(0)
project = Xcodeproj::Project.new("#{root}/Probe.xcodeproj")
target = project.new_target(:command_line_tool, 'Probe', :osx, '15.0')
file = project.main_group.new_file('main.m')
target.source_build_phase.add_file_reference(file)
target.source_build_phase.add_file_reference(project.main_group.new_file('Probe.swift'))
target.build_configurations.each do |config|
  config.build_settings['CLANG_ENABLE_MODULES'] = 'YES'
  config.build_settings['SWIFT_VERSION'] = '5.0'
  config.build_settings['CODE_SIGNING_ALLOWED'] = 'NO'
end
project.save
scheme = Xcodeproj::XCScheme.new
scheme.add_build_target(target)
scheme.save_as(project.path, "Probe", true)
RUBY
for phase in cold warm; do
  # A new CI machine has no local CAS. Only the closed Layer Cache store survives.
  rm -rf "$evidence/native-cas"
  "$binary" xcode run \
    --data-dir "$evidence/store" --socket "$evidence/socket/cache.sock" \
    --project github.com/ziahamza/layercache --compatibility "$abi" \
    --stats-file "$evidence/$phase-stats.json" \
    -- xcodebuild -project "$evidence/project/Probe.xcodeproj" -scheme Probe \
    -configuration Release -derivedDataPath "$evidence/derived-$phase" \
    "COMPILATION_CACHE_CAS_PATH=$evidence/native-cas" COMPILER_INDEX_STORE_ENABLE=NO \
    build > "$evidence/$phase.log" 2>&1 || { tail -n 100 "$evidence/$phase.log"; exit 1; }
  cat "$evidence/$phase-stats.json"
  "$evidence/derived-$phase/Build/Products/Release/Probe" > "$evidence/$phase-output.log" 2>&1
  if [[ "$phase" == cold && -n "$pressure_proxy" ]]; then
    # Copy only after the adapter exits and closes its SQLite database.
    cp -R "$evidence/store" "$evidence/pressure-store"
  fi
done
python3 - "$evidence" <<'PY'
import json,sys
from pathlib import Path
root=Path(sys.argv[1])
warm=json.loads((root/'warm-stats.json').read_text())
assert warm['actionHits'] > 0, f'no warm compilation action hits: {warm}'
assert warm['casHits'] > 0, f'no warm compiler objects replayed: {warm}'
assert '** BUILD SUCCEEDED **' in (root/'warm.log').read_text()
assert 'Layer Cache compiler probe 42' in (root/'warm-output.log').read_text()
print('Fresh DerivedData replayed compiler actions and CAS objects through Layer Cache.')
PY

if [[ -n "$pressure_proxy" ]]; then
  # Plan a tight quota from the closed cold store without opening a Store or
  # issuing CAS reads. The slack admits a pressure object beside a leased graph.
  python3 - "$evidence" <<'PY'
import json,sqlite3,sys
from pathlib import Path
root=Path(sys.argv[1])
db=sqlite3.connect(f'file:{root / "pressure-store/metadata.db"}?mode=ro',uri=True)
usage=db.execute('SELECT COALESCE(SUM(size),0) FROM artifacts').fetchone()[0]
largest=db.execute("SELECT COALESCE(MAX(size),0) FROM cache_entries WHERE integration='xcode' AND native_key LIKE 'cas-%'").fetchone()[0]
db.close()
assert usage > 0 and largest > 0, 'cold compiler store has no CAS objects'
quota=usage+largest+max(1<<20,usage//3)
assert quota <= 2<<30, f'cold probe store too large for bounded QA pressure: {quota}'
(root/'pressure-plan.json').write_text(json.dumps({'coldUsageBytes':usage,'largestObjectBytes':largest,'quotaBytes':quota},indent=2)+'\n')
(root/'pressure-quota.txt').write_text(str(quota)+'\n')
PY
  quota="$(cat "$evidence/pressure-quota.txt")"
  rm -rf "$evidence/native-cas"
  "$binary" xcode run \
    --data-dir "$evidence/pressure-store" --socket "$evidence/socket/backend.sock" \
    --max-size "$quota" \
    --project github.com/ziahamza/layercache --compatibility "$abi" \
    --stats-file "$evidence/pressure-stats.json" \
    -- "$pressure_proxy" --store "$evidence/pressure-store" --quota "$quota" \
    --socket "$evidence/socket/pressure.sock" --stats "$evidence/pressure-proxy.json" \
    -- xcodebuild -project "$evidence/project/Probe.xcodeproj" -scheme Probe \
    -configuration Release -derivedDataPath "$evidence/derived-pressure" \
    "COMPILATION_CACHE_CAS_PATH=$evidence/native-cas" COMPILER_INDEX_STORE_ENABLE=NO \
    build > "$evidence/pressure.log" 2>&1 || { tail -n 100 "$evidence/pressure.log"; exit 1; }
  "$evidence/derived-pressure/Build/Products/Release/Probe" > "$evidence/pressure-output.log" 2>&1
  python3 - "$evidence" <<'PY'
import json,sqlite3,sys
from pathlib import Path
root=Path(sys.argv[1])
pressure=json.loads((root/'pressure-proxy.json').read_text())
stats=json.loads((root/'pressure-stats.json').read_text())
assert pressure['pressurePayloadBytes'] > pressure['quotaBytes'], pressure
assert pressure['evictedBytes'] > 0, pressure
assert pressure['maxObservedUsageBytes'] <= pressure['quotaBytes'], pressure
assert pressure['compilerRootReadsAfterPressure'] > 0, pressure
assert pressure['compilerRootGetsAfterPressure'] > 0, pressure
assert pressure['pressureDurationNanos'] > 0, pressure
assert pressure['pressureDurationNanos'] <= pressure['queryToFirstRootFetchNanos'] < 120_000_000_000, pressure
assert pressure['compilerGraphReads'] > 0, pressure
assert pressure['fetchedPromisedObjects'] == pressure['promisedObjects'] > 0, pressure
assert not pressure['unfetchedPromisedObjects'], pressure
assert not pressure['missingPromisedObjects'], pressure
assert not pressure.get('pressureError'), pressure
assert stats['actionHits'] > 0 and stats['casHits'] > 0, stats
assert not stats['uncachedFallback'] and not stats['uncachedRetry'], stats
assert '** BUILD SUCCEEDED **' in (root/'pressure.log').read_text()
assert 'Layer Cache compiler probe 42' in (root/'pressure-output.log').read_text()
db=sqlite3.connect(f'file:{root / "pressure-store/metadata.db"}?mode=ro',uri=True)
usage=db.execute('SELECT COALESCE(SUM(size),0) FROM artifacts').fetchone()[0]
db.close()
assert usage <= pressure['quotaBytes'], (usage,pressure)
pressure['closedUsageBytes']=usage
(root/'pressure-proxy.json').write_text(json.dumps(pressure,indent=2)+'\n')
print('Real compiler restored its promised CAS graph after writes forced quota eviction.')
PY
fi
