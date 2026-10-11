#!/usr/bin/env bash
set -euo pipefail
binary="${1:?Layer Cache binary required}"
evidence="${2:?evidence directory required}"
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
done
python3 - "$evidence" <<'PY'
import json,sys
from pathlib import Path
root=Path(sys.argv[1])
warm=json.loads((root/'warm-stats.json').read_text())
assert warm['actionHits'] > 0, f'no warm compilation action hits: {warm}'
assert warm['casHits'] > 0, f'no warm compiler objects replayed: {warm}'
assert '** BUILD SUCCEEDED **' in (root/'warm.log').read_text()
print('Fresh DerivedData replayed compiler actions and CAS objects through Layer Cache.')
PY
