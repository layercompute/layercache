import assert from 'node:assert/strict';
import test from 'node:test';
import { parseGradleInfo } from './info.mjs';

const first = 'a'.repeat(32);
const second = 'b'.repeat(32);
const third = 'c'.repeat(32);
const keyLine = (path, key = first) => `Build cache key for task '${path}' is ${key}\n`;
const sorted = values => [...values].sort();

function unavailable(result, reason) {
  assert.equal(result.keys.size, 0);
  assert.equal(result.ambiguousPaths.size, 0);
  assert.equal(result.duplicateKeys.size, 0);
  assert.equal(result.reason, reason);
}

test('matches exact native lines without relying on adjacent task or HTTP output', () => {
  const text = [
    '> Task :app:compileDebugKotlin\n',
    keyLine(':app:compileDebugKotlin', first),
    '> Task :other:compileDebugJavaWithJavac\n',
    'Downloading an unrelated dependency\n',
    keyLine(':included:app:compileDebugKotlin', second),
    keyLine(':other:compileDebugJavaWithJavac', third),
    'BUILD SUCCESSFUL in 2s\n',
  ].join('');
  const result = parseGradleInfo(text);
  assert.deepEqual(sorted(result.keys), sorted([
    [':app:compileDebugKotlin', first],
    [':included:app:compileDebugKotlin', second],
    [':other:compileDebugJavaWithJavac', third],
  ]));
  assert.equal(result.ambiguousPaths.size, 0);
  assert.equal(result.duplicateKeys.size, 0);
  assert.equal(result.reason, undefined);
});

test('parallel order, included-build identity and CRLF do not change matching', () => {
  const lines = [keyLine(':app:compile', first), keyLine(':tools:app:compile', second), keyLine(':buildSrc:compile', third)];
  const forward = parseGradleInfo(lines.join(''));
  const reverse = parseGradleInfo(lines.toReversed().join('').replaceAll('\n', '\r\n'));
  assert.deepEqual(sorted(forward.keys), sorted(reverse.keys));
  assert.equal(forward.keys.size, 3);
});

test('identical repeated lines safely deduplicate one path and key', () => {
  const result = parseGradleInfo(keyLine(':app:compile').repeat(3));
  assert.deepEqual([...result.keys], [[':app:compile', first]]);
  assert.equal(result.ambiguousPaths.size, 0);
  assert.equal(result.duplicateKeys.size, 0);
});

test('conflicting keys exclude their path without discarding another exact path mapping', () => {
  const lines = [
    keyLine(':app:ambiguous', first), keyLine(':app:ambiguous', second),
    keyLine(':included:sharesFirst', first), keyLine(':app:valid', third),
  ];
  for (const text of [lines.join(''), lines.toReversed().join('')]) {
    const result = parseGradleInfo(text);
    assert.deepEqual(sorted(result.keys), sorted([[':included:sharesFirst', first], [':app:valid', third]]));
    assert.deepEqual([...result.ambiguousPaths], [':app:ambiguous']);
    assert.deepEqual([...result.duplicateKeys], [first]);
    assert.equal(result.reason, undefined);
  }
});

test('equivalent task paths can share a native key while producer binding remains a server decision', () => {
  const result = parseGradleInfo(keyLine(':app:compile') + keyLine(':included:compile'));
  assert.equal(result.keys.size, 2);
  assert.equal(result.ambiguousPaths.size, 0);
  assert.deepEqual([...result.duplicateKeys], [first]);
  assert.equal(result.reason, undefined);
});

test('explicit native disabled markers establish ineligibility without retaining private reasons', () => {
  const result = parseGradleInfo("Caching disabled for task ':uncacheable' because:\n  Private reason and path stay local\n" + keyLine(':cacheable'));
  assert.deepEqual([...result.ineligiblePaths], [':uncacheable']);
  assert.equal(result.keys.get(':cacheable'), first);
  assert.equal(JSON.stringify(result).includes('Private reason'), false);
  const conflict = parseGradleInfo(keyLine(':conflict') + "Caching disabled for task ':conflict' because:\n");
  assert.equal(conflict.keys.size, 0);assert.equal(conflict.ineligiblePaths.size, 0);assert.ok(conflict.ambiguousPaths.has(':conflict'));
  unavailable(parseGradleInfo("prefix Caching disabled for task ':x' because:\n"), 'unrecognized-key-line');
});

test('prefixed, ANSI, noncanonical hashes and incomplete task-key messages fail closed', () => {
  const valid = keyLine(':app:valid');
  const malformed = [
    `prefix ${keyLine(':app:compile')}`,
    `\x1b[32m${keyLine(':app:compile')}`,
    keyLine(':app:compile').trimEnd() + '\x1b[0m\n',
    keyLine(':app:compile', first.slice(1)),
    keyLine(':app:compile', first + '0'),
    keyLine(':app:compile', first.toUpperCase()),
    keyLine(':app:compile', 'g'.repeat(32)),
    keyLine(':app:compile', 'sha256:' + first),
    `Build cache key for task ':app:compile' is \n`,
    `Build cache key for task ':app:compile\n`,
    `Build cache key for task\n`,
    keyLine(':app:compile').trimEnd() + ' trailing text\n',
  ];
  for (const line of malformed) unavailable(parseGradleInfo(valid + line), 'unrecognized-key-line');
});

test('task paths remain exact local identities and reject controls or excessive length', () => {
  const path = ':' + 'a'.repeat(1023);
  assert.equal(parseGradleInfo(keyLine(path)).keys.get(path), first);
  unavailable(parseGradleInfo(keyLine(path + 'b')), 'invalid-task-path');
  for (const control of ['\0', '\t', '\x1b', '\x7f', '\x85', '\u202e', '\u2028', '\u2029']) {
    unavailable(parseGradleInfo(keyLine(`:app:${control}compile`)), 'invalid-task-path');
  }
  const unicode = ':caf\u00e9:compile';
  assert.equal(parseGradleInfo(keyLine(unicode)).keys.get(unicode), first);
});

test('unsupported versions and invalid or missing input return fixed reasons', () => {
  for (const version of ['9.3', '9.3.0', '9.3.2', '9.4', '9.4.0', '9.4.2', '9.5.0', '9.3.1-custom', '9.4.1-custom', null, 9]) {
    unavailable(parseGradleInfo(keyLine(':app:compile'), { gradleVersion: version }), 'unsupported-gradle-version');
  }
  for (const input of [undefined, null, 3, {}, Buffer.from(keyLine(':app:compile'))]) {
    unavailable(parseGradleInfo(input), 'invalid-info-log');
  }
  for (const text of ['', 'BUILD SUCCESSFUL\n', 'Build cache key for transform artifact.jar is ' + first + '\n']) {
    unavailable(parseGradleInfo(text), 'missing-key-lines');
  }
  assert.equal(parseGradleInfo(keyLine(':app:compile'), { gradleVersion: undefined }).keys.size, 1);
});

test('each qualified version keeps exact keys, disabled markers and malformed-input refusal', () => {
  for (const gradleVersion of ['9.3.1', '9.4.1']) {
    const parse = text => parseGradleInfo(text, {gradleVersion});
    const valid = keyLine(':root:compile') + keyLine(':included:compile') + "Caching disabled for task ':uncacheable' because:\n";
    const result = parse(valid);
    assert.deepEqual([...result.keys], [[':root:compile', first], [':included:compile', first]]);
    assert.deepEqual([...result.ineligiblePaths], [':uncacheable']);
    assert.deepEqual([...result.duplicateKeys], [first]);
    for (const malformed of [
      'prefix ' + keyLine(':wrong'), keyLine(':wrong', first.toUpperCase()),
      keyLine(':wrong', first.slice(1)), "Caching disabled for task ':wrong' because: extra\n",
      keyLine(':wrong').trimEnd(),
    ]) {
      const rejected = parse(valid + malformed);
      assert.equal(rejected.keys.size, 0);assert.equal(rejected.ineligiblePaths.size, 0);
      assert.ok(['unrecognized-key-line', 'truncated-info-log'].includes(rejected.reason));
    }
    const conflict = parse(keyLine(':same', first) + keyLine(':same', second));
    assert.equal(conflict.keys.size, 0);assert.ok(conflict.ambiguousPaths.has(':same'));
  }
});

test('a nonterminated final line invalidates prior mappings', () => {
  unavailable(parseGradleInfo(keyLine(':app:compile').trimEnd()), 'truncated-info-log');
  unavailable(parseGradleInfo(keyLine(':app:compile') + 'BUILD SUCC'), 'truncated-info-log');
});

test('bounds use UTF-8 bytes and include repeated matching lines', () => {
  unavailable(parseGradleInfo('x'.repeat(64 * 1024 * 1024 + 1)), 'info-log-too-large');
  unavailable(parseGradleInfo('\u20ac'.repeat(Math.floor(64 * 1024 * 1024 / 3) + 1)), 'info-log-too-large');
  assert.equal(parseGradleInfo(keyLine(':app:compile').repeat(8192)).keys.size, 1);
  unavailable(parseGradleInfo(keyLine(':app:compile').repeat(8193)), 'too-many-key-lines');
});

test('bounds count every distinct path even when producer keys are duplicated', () => {
  const text = Array.from({ length: 4096 }, (_, i) => keyLine(`:app:task${i}`, i.toString(16).padStart(32, '0'))).join('');
  assert.equal(parseGradleInfo(text).keys.size, 4096);
  unavailable(parseGradleInfo(text + keyLine(':app:task4096', 'd'.repeat(32))), 'too-many-task-paths');
  const duplicated = Array.from({ length: 4097 }, (_, i) => keyLine(`:app:task${i}`)).join('');
  unavailable(parseGradleInfo(duplicated), 'too-many-task-paths');
});

test('irrelevant sensitive-looking content is ignored and never included in diagnostics', () => {
  const privateText = 'operator-token=<private value> /private/worktree/source.java';
  const result = parseGradleInfo(privateText + '\n' + keyLine(':app:compile'));
  assert.deepEqual([...result.keys], [[':app:compile', first]]);
  assert.equal(JSON.stringify(result).includes(privateText), false);
  const rejected = parseGradleInfo(`Build cache key for task '${privateText}' is invalid\n`);
  unavailable(rejected, 'unrecognized-key-line');
  assert.equal(JSON.stringify(rejected).includes(privateText), false);
});
