// Small native Gradle workloads qualify the public completion listener and its
// optional info-log projection. No Android or hosted application jobs run here.
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {chmodSync, closeSync, mkdirSync, mkdtempSync, openSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname, isAbsolute, join, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseGradleInfo} from '../../action/gradle/info.mjs';
import {projectGradleInvocation} from '../../action/gradle/reports.mjs';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const root = mkdtempSync(join(tmpdir(), 'layercache-gradle-metering-'));
chmodSync(root, 0o700);
const workspace = join(root, 'workspace'), reports = join(root, 'reports');
mkdirSync(workspace, {mode: 0o700});mkdirSync(reports, {mode: 0o700});
const gradle = process.env.GRADLE_BIN ?? 'gradle';
const project = 'synthetic-gradle-project', compatibility = 'synthetic-gradle931-jdk17';
const init = join(repository, 'native/gradle/init.gradle');
const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => ['HOME', 'PATH', 'JAVA_HOME', 'USER', 'LOGNAME', 'LANG', 'LC_ALL', 'TMPDIR'].includes(name)));
Object.assign(environment, {GRADLE_USER_HOME: join(root, 'gradle-home'), CI: 'true', TERM: 'dumb',
  LAYER_CACHE_GRADLE_REPORT_DIR: reports, LAYER_CACHE_GRADLE_REPORT_PROJECT: project, LAYER_CACHE_GRADLE_REPORT_COMPATIBILITY: compatibility});
const stages = [];
const taskSource = `
import org.gradle.api.provider.Property
import org.gradle.api.file.RegularFileProperty
@CacheableTask
abstract class MeteredText extends DefaultTask {
  @Input abstract Property<String> getContent()
  @Input abstract Property<Boolean> getFailFixture()
  @OutputFile abstract RegularFileProperty getResultFile()
  @TaskAction void produce() {
    if (failFixture.get()) throw new GradleException('Deliberate metering fixture failure')
    def output = resultFile.get().asFile
    output.parentFile.mkdirs()
    output.text = content.get()
  }
}
allprojects {
  tasks.register('cached', MeteredText) { content.set(project.path); failFixture.set(false); resultFile.set(layout.buildDirectory.file('cached.txt')) }
  tasks.register('always') { doLast { logger.lifecycle('SYNTHETIC_CURRENT_TASK_EXECUTED') } }
  tasks.register('skip') { onlyIf { false }; doLast { throw new GradleException('Skipped fixture executed') } }
  tasks.register('empty', Copy) { from(layout.projectDirectory.dir('missing')); into(layout.buildDirectory.dir('empty')) }
  tasks.register('fail', MeteredText) { content.set('fail'); failFixture.set(true); resultFile.set(layout.buildDirectory.file('failed.txt')) }
  tasks.register('duplicateA', MeteredText) { content.set('same'); failFixture.set(false); resultFile.set(layout.buildDirectory.file('a.txt')) }
  tasks.register('duplicateB', MeteredText) { content.set('same'); failFixture.set(false); resultFile.set(layout.buildDirectory.file('b.txt')) }
}
`;
writeFileSync(join(workspace, 'settings.gradle'), "rootProject.name='metering-main'\ninclude('one','two')\nincludeBuild('included')\n");
writeFileSync(join(workspace, 'build.gradle'), taskSource);
for (const name of ['one', 'two', 'included', 'buildSrc']) mkdirSync(join(workspace, name));
writeFileSync(join(workspace, 'included/settings.gradle'), "rootProject.name='included'\n");
writeFileSync(join(workspace, 'included/build.gradle'), taskSource);
writeFileSync(join(workspace, 'buildSrc/build.gradle'), "plugins { id 'java' }\n");
mkdirSync(join(workspace, 'buildSrc/src/main/java'), {recursive: true});
writeFileSync(join(workspace, 'buildSrc/src/main/java/SyntheticMarker.java'), 'public final class SyntheticMarker {}\n');
const selected = [':cached', ':one:cached', ':two:cached', ':included:cached', ':always', ':skip', ':empty'];

function execute(name, args, {metered = true, oldInit, expectFailure = false, home} = {}) {
  const before = new Set(readdirSync(reports));
  const begin = performance.now();
  const result = spawnSync(gradle, ['--no-daemon', '--console=plain', '--info', '--parallel', '--max-workers=2',
    '-Dorg.gradle.java.installations.auto-download=false', '-I', oldInit ?? init, ...args],
  {cwd: workspace, env: {...environment, ...(home ? {GRADLE_USER_HOME: home} : {}), ...(!metered ? {LAYER_CACHE_GRADLE_REPORT_DIR: ''} : {})},
    encoding: 'utf8', timeout: 120000, maxBuffer: 8 * 1024 ** 2});
  if (result.error) throw result.error;
  const text = result.stdout + result.stderr;
  const log = join(root, `${name}.log`);writeFileSync(log, text, {mode: 0o600});
  assert.equal(result.status !== 0, expectFailure, `${name} unexpected exit; inspect ${log}`);
  const fresh = readdirSync(reports).filter(file => !before.has(file) && file.endsWith('.json'));
  let document, projection;
  if (metered) {
    assert.equal(fresh.length, 1, `${name} needs exactly one root listener report`);
    document = JSON.parse(readFileSync(join(reports, fresh[0]), 'utf8'));
    assert.equal(document.truncated, false);
    projection = projectGradleInvocation(document, {project, compatibility, infoText: text, infoMtimeMs: statSync(log).mtimeMs});
    assert.equal(JSON.stringify(projection.body).includes('taskPath'), false);
    assert.equal(new Set(document.tasks.map(task => task.workRef)).size, document.tasks.length);
  }
  stages.push({name, status: result.status, elapsedMs: Math.round(performance.now() - begin),
    ...(document ? {tasks: document.tasks.length, counts: Object.fromEntries(['executed', 'fromCache', 'upToDate', 'noSource', 'skipped', 'failed'].map(outcome => [outcome, document.tasks.filter(task => task.outcome === outcome).length])),
      keyed: projection.body.tasks.filter(task => task.nativeKey).length, knownIneligible: projection.body.tasks.filter(task => task.cacheEligibility === 'ineligible').length,
      configurationCacheReused: text.includes('Reusing configuration cache.')} : {})});
  console.log(`${name}: passed`);
  return {text, document, projection};
}
function clearOutputs({configuration = false} = {}) {
  for (const base of ['', 'one', 'two', 'included', 'buildSrc']) {
    rmSync(join(workspace, base, 'build'), {recursive: true, force: true});
    if (configuration) rmSync(join(workspace, base, '.gradle'), {recursive: true, force: true});
  }
}
function task(result, path) {
  const index = result.document.tasks.findIndex(row => row.taskPath === path);
  assert.ok(index >= 0, `Missing native task ${path}`);
  return {raw: result.document.tasks[index], wire: result.projection.body.tasks[index]};
}

try {
  const version = spawnSync(gradle, ['--version'], {env: environment, encoding: 'utf8', timeout: 30000});
  assert.equal(version.status, 0);assert.match(version.stdout, /Gradle 9\.3\.1(?:\s|$)/);
  const original = spawnSync('git', ['show', 'f49b92a:native/gradle/init.gradle'], {cwd: repository, encoding: 'utf8'});
  assert.equal(original.status, 0);
  const oldInit = join(root, 'original-init.gradle');writeFileSync(oldInit, original.stdout, {mode: 0o600});
  const baseline = execute('original-init-native-keys', ['--no-configuration-cache', '--build-cache', ...selected], {metered: false, oldInit, home: join(root, 'original-gradle-home')});
  const originalKeys = parseGradleInfo(baseline.text).keys;
  clearOutputs({configuration: true});
  const cold = execute('root-multi-composite-parallel-cold', ['--configuration-cache', '--build-cache', ...selected]);
  for (const path of [':cached', ':one:cached', ':two:cached', ':included:cached']) {
    assert.equal(task(cold, path).wire.nativeKey, originalKeys.get(path), 'Observation must preserve the native cache key');
    assert.equal(task(cold, path).raw.outcome, 'executed');
  }
  assert.ok(cold.document.tasks.some(row => row.taskPath.startsWith(':buildSrc:')), 'Root listener must include buildSrc without duplicates');
  assert.equal(task(cold, ':always').wire.cacheEligibility, 'ineligible');
  assert.equal(task(cold, ':skip').raw.outcome, 'skipped');
  assert.equal(task(cold, ':empty').raw.outcome, 'noSource');
  const reused = execute('configuration-cache-reused', ['--configuration-cache', '--build-cache', ...selected]);
  assert.ok(reused.text.includes('Reusing configuration cache.'));
  assert.notEqual(reused.document.invocationId, cold.document.invocationId);
  for (const path of [':cached', ':one:cached', ':two:cached', ':included:cached']) assert.equal(task(reused, path).raw.outcome, 'upToDate');
  clearOutputs();
  const restored = execute('configuration-cache-accepted-restores', ['--configuration-cache', '--build-cache', ...selected]);
  for (const path of [':cached', ':one:cached', ':two:cached', ':included:cached']) {
    assert.equal(task(restored, path).raw.outcome, 'fromCache');
    assert.equal(task(restored, path).wire.cacheEligibility, 'eligible');
    assert.equal(task(restored, path).wire.nativeKey, task(cold, path).wire.nativeKey);
  }
  clearOutputs();
  const bypass = execute('native-cache-disabled-execution', ['--configuration-cache', '--no-build-cache', ...selected]);
  for (const path of [':cached', ':one:cached', ':two:cached', ':included:cached']) {
    assert.equal(task(bypass, path).raw.outcome, 'executed');assert.equal(task(bypass, path).wire.cacheEligibility, 'ineligible');
    assert.equal(task(bypass, path).wire.nativeKey, undefined);
  }
  const failed = execute('failed-native-task', ['--configuration-cache', '--build-cache', ':fail'], {expectFailure: true});
  assert.equal(task(failed, ':fail').raw.outcome, 'failed');assert.equal(task(failed, ':fail').wire.nativeKey, undefined);
  const duplicate = execute('equivalent-tasks-share-native-key', ['--configuration-cache', '--build-cache', ':duplicateA', ':duplicateB']);
  assert.equal(task(duplicate, ':duplicateA').wire.nativeKey, task(duplicate, ':duplicateB').wire.nativeKey);
  assert.match(task(duplicate, ':duplicateA').wire.nativeKey, /^[a-f0-9]{32}$/);
  const receipt = {version: 1, passed: true, gradleVersion: '9.3.1', stages, boundaries: {nativeKeysUnchanged: true, exactTaskOutcomes: true,
    completeTaskGraph: false, httpSourceAttribution: false, applicationBuildSavings: false}};
  const encoded = JSON.stringify(receipt, null, 2) + '\n';
  writeFileSync(join(root, 'evidence.json'), encoded, {mode: 0o600});
  if (process.env.GRADLE_METERING_QA_OUTPUT) {
    assert.ok(isAbsolute(process.env.GRADLE_METERING_QA_OUTPUT), 'GRADLE_METERING_QA_OUTPUT must be an absolute new path');
    const output = openSync(process.env.GRADLE_METERING_QA_OUTPUT, 'wx', 0o600);
    try {writeFileSync(output, encoded);} finally {closeSync(output);}
  }
  console.log(JSON.stringify({passed: true, stages: stages.length, privateEvidence: join(root, 'evidence.json')}));
} catch (error) {
  console.error(`Gradle metering qualification failed; inspect ${root}`);
  throw error;
}
