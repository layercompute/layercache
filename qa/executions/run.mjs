import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {createServer} from 'node:http';
import {readFile, mkdir, mkdtemp, writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import os from 'node:os';
import {entry, inventory, job, now, project, volume} from './fixtures.mjs';

// The override allows an isolated worktree to reuse an already installed toolchain.
// It selects dependencies only, never a dashboard implementation or live service.
const require = createRequire(process.env.EXECUTION_QA_PACKAGE_FILE || import.meta.url);
const {build} = require('esbuild');
const engines = require('@playwright/test');
const root = fileURLToPath(new URL('../../', import.meta.url));
const output = process.env.QA_OUTPUT || await mkdtemp(path.join(os.tmpdir(), 'layercache-executions-'));
await mkdir(output, {recursive: true});
const selected = (process.env.EXECUTION_QA_BROWSERS || 'chromium').split(',');
if (!selected.length || selected.some(value => !['chromium', 'firefox', 'webkit'].includes(value))) {
  throw new Error('EXECUTION_QA_BROWSERS must contain chromium, firefox, or webkit');
}
const compiled = await build({
  absWorkingDir: root, entryPoints: ['qa/executions/fixture.ts'], bundle: true,
  write: false, target: 'es2022', platform: 'browser', format: 'iife',
});
const script = compiled.outputFiles[0].contents;
const styles = {
  local: await readFile(path.join(root, 'internal/dashboard/assets/style.css')),
  hosted: await readFile(path.join(root, 'internal/portal/assets/style.css')),
};
const server = createServer((request, response) => {
  const url = new URL(request.url || '/', 'http://127.0.0.1');
  if (url.pathname === '/fixture.js') {
    response.writeHead(200, {'Content-Type': 'text/javascript'}).end(script);
  } else if (url.pathname === '/style.css' && (url.searchParams.get('mode') || '') in styles) {
    response.writeHead(200, {'Content-Type': 'text/css'}).end(styles[url.searchParams.get('mode')]);
  } else if (url.pathname === '/') {
    const mode = url.searchParams.get('mode') === 'hosted' ? 'hosted' : 'local';
    response.writeHead(200, {'Content-Type': 'text/html'}).end(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Execution view fixture</title><link rel="stylesheet" href="/style.css?mode=${mode}"><script defer src="/fixture.js"></script></head><body>${mode === 'local' ? '<main><div class="content" id="fixture"></div></main>' : '<div class="layout"><nav aria-label="Fixture navigation"></nav><main id="fixture"></main></div>'}</body></html>`);
  } else response.writeHead(404).end();
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const address = server.address();
if (!address || typeof address === 'string') throw new Error('Fixture server did not bind');
const origin = `http://127.0.0.1:${address.port}`;
const results = [];
const failures = [];

try {
  for (const engine of selected) {
    const browser = await engines[engine].launch({headless: true, ...(engine === 'chromium' ? {args: ['--no-sandbox']} : {})});
    try {
      for (const mode of ['local', 'hosted']) {
        const context = await browser.newContext({viewport: {width: 1440, height: 1050}});
        const page = await context.newPage();
        const pageErrors = [];
        const foreignRequests = [];
        page.on('pageerror', error => pageErrors.push(error.message));
        await page.route('**/*', route => {
          if (new URL(route.request().url()).origin !== origin) {
            foreignRequests.push(route.request().url());
            return route.abort();
          }
          return route.continue();
        });
        await page.goto(`${origin}/?mode=${mode}`);
        await page.waitForFunction(() => typeof window.renderExecutionFixture === 'function');
        const render = async projects => page.evaluate(({projects, now}) => window.renderExecutionFixture(projects, now), {projects, now});
        const region = page.getByRole('region', {name: 'Where used', exact: true});
        async function check(name, action) {
          const label = `${engine}/${mode}/${name}`;
          try {
            await action();
            results.push({name: label, passed: true});
          } catch (error) {
            const detail = error instanceof Error ? error.message : String(error);
            failures.push(`${label}: ${detail}`);
            results.push({name: label, passed: false, error: detail});
            await page.screenshot({path: path.join(output, label.replaceAll('/', '-') + '.png'), fullPage: true});
          }
        }
        await check('one installation across two projects', async () => {
          await render([project('example/first', inventory({installations: [entry()]})), project('example/second', inventory({installations: [entry()]}))]);
          assert.equal(await region.locator('.execution-installation').count(), 1);
          const card = region.locator('.execution-installation').first();
          assert.match(await card.innerText(), /example\/first/);
          assert.match(await card.innerText(), /example\/second/);
          assert.match(await card.locator('.execution-projects').innerText(), /^(?:2 projects|Projects) · example\/first, example\/second$/);
          assert.equal(await card.getAttribute('data-execution-id'), entry().id);
        });
        await check('equal project names preserve distinct project identities', async () => {
          await render([project('example/first', inventory({installations: [entry()]}), 'Shared name'), project('example/second', inventory({installations: [entry()]}), 'Shared name')]);
          assert.equal(await region.locator('.execution-installation').count(), 1);
          const text = await region.locator('.execution-installation .execution-projects').innerText();
          assert.ok(text.includes('example/first'), text);
          assert.ok(text.includes('example/second'), text);
        });
        await check('retained cache activity stays independent of latest resource facts', async () => {
          const older = entry({lastObservedAt: '2026-10-20T10:00:00Z', cacheActivity: 'observed'});
          older.sample.cpu.visibleLogical = 32;
          older.sample.cpu.affinityLogical = 12;
          older.sample.cpu.quotaMilliCPU = 6000;
          older.sample.cpu.effectiveMilliCPU = 6000;
          older.sample.memory.visibleTotalBytes = 96 * 1024 ** 3;
          older.sample.memory.cgroupLimitBytes = 12 * 1024 ** 3;
          older.sample.memory.effectiveLimitBytes = 12 * 1024 ** 3;
          older.sample.filesystems = [volume({roles: ['localCache'], capacityBytes: 200 * 1024 ** 3, freeBytes: 80 * 1024 ** 3, availableBytes: 65 * 1024 ** 3})];
          const newer = entry({lastObservedAt: '2026-10-20T11:00:00Z', cacheActivity: 'unconfirmed'});
          newer.sample.filesystems = [volume({roles: ['workspace']})];
          for (const pair of [[newer, older], [older, newer]]) {
            await render([project('example/first', inventory({installations: [pair[0]]})), project('example/second', inventory({installations: [pair[1]]}))]);
            assert.equal(await region.locator('.execution-installation').count(), 1);
            const card = region.locator('.execution-installation');
            assert.match(await card.locator('.execution-sampled').innerText(), /2026-10-20 11:00:00 UTC/);
            assert.equal(await card.locator('.execution-cache-activity').innerText(), 'Cache activity observed');
            const resources = card.locator('> .execution-details > .execution-resources');
            for (const [label, expected] of [['Visible logical CPUs', '16'], ['CPU affinity', '8'], ['CPU quota', '2.5 vCPU'], ['Effective CPU limit', '2.5 vCPU'], ['Visible memory', '64.0 GiB'], ['Container memory limit', '8.0 GiB'], ['Effective memory limit', '8.0 GiB']]) {
              const value = await resources.getByText(label, {exact: true}).evaluate(node => node.parentElement?.querySelector('dd')?.textContent);
              assert.equal(value, expected);
            }
            const disk = card.locator('.execution-volume');
            assert.equal(await disk.count(), 1);
            assert.equal(await disk.getByText('Capacity', {exact: true}).count(), 1);
            const roles = await disk.locator('.execution-volume-name').innerText();
            assert.match(roles, /workspace/i);
            assert.match(roles, /local cache/i);
            for (const [label, amount] of [['Capacity', 100], ['Used', 70], ['Free', 30], ['Available', 25], ['Reserved', 5]]) {
              const value = await disk.getByText(label, {exact: true}).evaluate(node => node.parentElement?.querySelector('dd')?.textContent);
              assert.match(value || '', new RegExp(`^${amount}(?:\\.0)? GiB(?: \\([0-9.]+%\\))?$`));
            }
          }
        });
        await check('duplicate volume is counted once', async () => {
          const observed = entry();
          observed.sample.filesystems = [volume({roles: ['workspace']}), volume({roles: ['localCache']})];
          await render([project('example/first', inventory({installations: [observed]})), project('example/second', inventory({installations: [observed]}))]);
          assert.equal(await region.locator('.execution-volume').count(), 1);
          const text = await region.locator('.execution-volume').innerText();
          assert.match(text, /workspace/i);
          assert.match(text, /local cache/i);
        });
        await check('protocol filesystem roles have readable labels', async () => {
          await render([project('example/roles', inventory({installations: [entry()]}))]);
          const roles = await region.locator('.execution-volume-name').innerText();
          for (const name of [/workspace/i, /local cache/i, /temporary data/i]) assert.match(roles, name);
        });
        await check('independent signed jobs stay separate', async () => {
          await render([project('example/first', inventory({jobs: [job(), job({id: 'f'.repeat(64), executionId: 'github-actions:1:100:1:201'})]}))]);
          assert.equal(await region.locator('.execution-job').count(), 2);
          assert.match(await region.innerText(), /GitHub Actions/i);
          const ids = await region.locator('.execution-job').evaluateAll(cards => cards.map(card => card.getAttribute('data-execution-id')));
          assert.deepEqual(new Set(ids), new Set(['e'.repeat(64), 'f'.repeat(64)]));
        });
        await check('owner collisions remain isolated', async () => {
          await render([project('example/first', inventory({installations: [entry()]})), project('example/second', inventory({installations: [entry({ownerId: 'github-id:2'})]}))]);
          assert.equal(await region.locator('.execution-installation').count(), 2);
          const labels = (await region.locator('.execution-installation .execution-projects').allInnerTexts()).sort();
          assert.equal(labels.length, 2);
          assert.match(labels[0], /^(?:1 project|Projects) · example\/first$/);
          assert.match(labels[1], /^(?:1 project|Projects) · example\/second$/);
        });
        await check('unknown resource limits stay unknown', async () => {
          const observed = entry();
          observed.sample.cpu = {visibleLogical: 16, affinityLogical: null, quotaMilliCPU: null, effectiveMilliCPU: null, limitKnowledge: 'partial'};
          observed.sample.memory = {visibleTotalBytes: 64 * 1024 ** 3, cgroupLimitBytes: null, effectiveLimitBytes: null, limitKnowledge: 'partial'};
          observed.sample.filesystems = [volume({capacityBytes: null, freeBytes: null, availableBytes: null})];
          await render([project('example/first', inventory({installations: [observed]}))]);
          const text = await region.locator('.execution-installation').innerText();
          assert.match(text, /effective CPU[\s\S]*Unknown|Unknown[\s\S]*effective CPU/i);
          assert.match(text, /effective memory[\s\S]*Unknown|Unknown[\s\S]*effective memory/i);
          assert.ok((text.match(/Unknown/g) || []).length >= 5, text);
          assert.doesNotMatch(await region.locator('.execution-volume').innerText(), /\b0 B\b/);
        });
        await check('visible resources and effective limits are distinct', async () => {
          await render([project('example/first', inventory({installations: [entry()]}))]);
          const resources = region.locator('.execution-installation > .execution-details > .execution-resources');
          for (const [label, expected] of [['Visible logical CPUs', '16'], ['CPU affinity', '8'], ['CPU quota', '2.5 vCPU'], ['Effective CPU limit', '2.5 vCPU'], ['Visible memory', '64.0 GiB'], ['Container memory limit', '8.0 GiB'], ['Effective memory limit', '8.0 GiB']]) {
            const value = await resources.getByText(label, {exact: true}).evaluate(node => node.parentElement?.querySelector('dd')?.textContent);
            assert.equal(value, expected);
          }
        });
        await check('declared provider does not replace orchestrator evidence', async () => {
          await render([project('example/first', inventory({jobs: [job({provider: 'ubicloud', providerEvidence: 'declared', runnerEnvironment: 'self-hosted'})]}))]);
          const text = await region.locator('.execution-job').innerText();
          assert.match(text, /Ubicloud/i);
          assert.match(text, /declared/i);
          assert.match(text, /GitHub Actions/i);
          assert.doesNotMatch(text, /(?:signed|verified)\s+Ubicloud|Ubicloud\s+(?:signed|verified)/i);
        });
        await check('job resources expand using the keyboard', async () => {
          await render([project('example/first', inventory({jobs: [job()]}))]);
          const details = region.locator('.execution-job .execution-details');
          assert.equal(await details.evaluate(node => node.open), false);
          await details.locator('summary').focus();
          await page.keyboard.press('Enter');
          assert.equal(await details.evaluate(node => node.open), true);
          assert.match(await details.innerText(), /Effective CPU limit[\s\S]*2\.5 vCPU/);
          await page.keyboard.press('Enter');
          assert.equal(await details.evaluate(node => node.open), false);
        });
        await check('stale observations do not claim offline', async () => {
          await render([project('example/first', inventory({installations: [entry({lastObservedAt: '2026-10-19T11:59:59Z'})]}))]);
          const text = await region.locator('.execution-installation').innerText();
          assert.match(text, /Sample older than 24 hours/i);
          assert.doesNotMatch(text, /offline/i);
        });
        await check('the twenty-four-hour boundary remains current', async () => {
          await render([project('example/first', inventory({installations: [entry({lastObservedAt: '2026-10-19T12:00:00Z'})]}))]);
          assert.equal(await region.locator('.execution-installation .execution-stale').count(), 0);
          assert.match(await region.locator('.execution-installation .execution-status').innerText(), /Recorded sample/);
        });
        await check('filesystem values are independent and never project sums', async () => {
          await render([project('example/first', inventory({installations: [entry()]})), project('example/second', inventory({installations: [entry()]}))]);
          const disk = region.locator('.execution-volume');
          const text = await disk.innerText();
          for (const [label, amount] of [['Capacity', 100], ['Used', 70], ['Free', 30], ['Available', 25], ['Reserved', 5]]) {
            const value = await disk.getByText(label, {exact: true}).evaluate(node => node.parentElement?.querySelector('dd')?.textContent);
            assert.match(value || '', new RegExp(`^${amount}(?:\\.0)? GiB(?: \\([0-9.]+%\\))?$`));
          }
          assert.doesNotMatch(text, /200(?:\.0)? GiB|140(?:\.0)? GiB|60(?:\.0)? GiB|50(?:\.0)? GiB|10(?:\.0)? GiB/);
          assert.match(await region.innerText(), /not.*(?:sum|add)|(?:shared|same).*filesystem/i);
        });
        await check('old server inventory stays unknown', async () => {
          await render([{id: 'example/old-server'}]);
          assert.match(await region.innerText(), /Unknown|unavailable|not.*(?:support|report)/i);
          assert.equal(await region.locator('.execution-installation,.execution-job').count(), 0);
          assert.doesNotMatch(await region.innerText(), /\b0 (?:installations|jobs)\b/i);
        });
        await check('environment observations do not imply cache activity', async () => {
          await render([project('example/first', inventory({installations: [entry()], jobs: [job({cacheActivity: 'observed'})]}))]);
          assert.match(await region.locator('.execution-installation .execution-cache-activity').innerText(), /Setup observed; cache activity not yet linked/);
          assert.equal(await region.locator('.execution-job .execution-cache-activity').innerText(), 'Cache activity observed');
          assert.match(await region.innerText(), /Observed cache activity does not measure task coverage or savings/);
        });
        await check('only the newest fifty entries per list render', async () => {
          const installations = Array.from({length: 53}, (_, i) => entry({
            id: i.toString(16).padStart(64, '0'), installationId: (100 + i).toString(16).padStart(64, '0'),
            lastObservedAt: new Date(now - i * 1000).toISOString(),
          })).reverse();
          const jobs = Array.from({length: 53}, (_, i) => job({
            id: (1000 + i).toString(16).padStart(64, '0'), executionId: `github-actions:1:100:1:${200 + i}`,
            lastObservedAt: new Date(now - i * 1000).toISOString(),
          })).reverse();
          await render([project('example/first', inventory({installations, jobs, historyTruncated: true}))]);
          for (const [selector, source] of [['.execution-installation', installations], ['.execution-job', jobs]]) {
            assert.equal(await region.locator(selector).count(), 50);
            const actual = await region.locator(selector).evaluateAll(cards => cards.map(card => card.getAttribute('data-execution-id')));
            const expected = [...source].sort((a, b) => Date.parse(b.lastObservedAt) - Date.parse(a.lastObservedAt)).slice(0, 50).map(value => value.id);
            assert.deepEqual(actual, expected);
          }
          assert.match(await region.innerText(), /Showing the newest 50 of 53 observed installations/);
          assert.match(await region.innerText(), /Showing the newest 50 of 53 observed jobs/);
        });
        await check('server list truncation is disclosed independently of history', async () => {
          await render([project('example/first', inventory({installations: [entry()], listTruncated: true, historyTruncated: false}))]);
          assert.equal(await region.locator('.execution-installation').count(), 1);
          const text = await region.locator('.execution-partial').innerText();
          assert.match(text, /Partial inventory/);
          assert.match(text, /returned rows are limited/);
        });
        await check('global job counts use distinct returned rows', async () => {
          const jobs = Array.from({length: 64}, (_, i) => job({
            id: (1000 + i).toString(16).padStart(64, '0'), executionId: `github-actions:1:100:1:${200 + i}`,
            lastObservedAt: new Date(now - i * 1000).toISOString(),
          }));
          await render([
            project('example/first', inventory({jobs, jobCount: 1000, listTruncated: true})),
            project('example/second', inventory({jobs, jobCount: 1000, listTruncated: true})),
          ]);
          assert.equal(await region.locator('.execution-counts').innerText(), '0 observed client installations · 64 observed CI jobs');
          assert.equal(await region.locator('.execution-job').count(), 50);
          assert.match(await region.locator('.execution-partial').innerText(), /Partial inventory.*returned rows are limited/);
          assert.match(await region.getByRole('region', {name: 'CI jobs', exact: true}).innerText(), /Showing the newest 50 of 64 observed jobs/);
        });
        await check('unordered inputs are sorted before the processing bound', async () => {
          const installations = Array.from({length: 257}, (_, i) => entry({
            id: i.toString(16).padStart(64, '0'), installationId: (100 + i).toString(16).padStart(64, '0'),
            lastObservedAt: new Date(now - i * 1000).toISOString(),
          })).reverse();
          const jobs = Array.from({length: 257}, (_, i) => job({
            id: (1000 + i).toString(16).padStart(64, '0'), executionId: `github-actions:1:100:1:${200 + i}`,
            lastObservedAt: new Date(now - i * 1000).toISOString(),
          })).reverse();
          await render([project('example/first', inventory({installations, jobs}))]);
          for (const [selector, source] of [['.execution-installation', installations], ['.execution-job', jobs]]) {
            const actual = await region.locator(selector).evaluateAll(cards => cards.map(card => card.getAttribute('data-execution-id')));
            const expected = [...source].sort((a, b) => Date.parse(b.lastObservedAt) - Date.parse(a.lastObservedAt)).slice(0, 50).map(value => value.id);
            assert.equal(actual.length, 50);
            assert.deepEqual(actual, expected);
          }
          assert.match(await region.locator('.execution-partial').innerText(), /returned rows are limited/);
        });
        await check('untrusted strings use text content', async () => {
          const attack = '<img src=x onerror="window.executionFixtureXss=1">';
          const observed = entry();
          observed.sample.os = attack;
          observed.sample.filesystems = [volume({roles: [attack]})];
          await render([project('example/xss', inventory({installations: [observed]}), attack)]);
          assert.ok((await region.innerText()).includes(attack));
          assert.equal(await region.locator('img,script,iframe,svg').count(), 0);
          assert.equal(await page.evaluate(() => window.executionFixtureXss), undefined);
        });
        for (const width of [320, 390, 1440]) await check(`viewport ${width}`, async () => {
          await page.setViewportSize({width, height: 900});
          const long = 'example/' + 'long-project-name-'.repeat(12);
          await render([project(long, inventory({installations: [entry()], jobs: [job({provider: 'ubicloud', providerEvidence: 'declared'})]}), 'Long project ' + 'name '.repeat(30))]);
          await region.locator('.execution-details').evaluateAll(nodes => nodes.forEach(node => {node.open = true;}));
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
          assert.deepEqual(await region.locator('.execution-installation,.execution-job,.execution-volume').evaluateAll(nodes => nodes.filter(node => node.scrollWidth > node.clientWidth).map(node => node.className)), []);
          await page.screenshot({path: path.join(output, `${engine}-${mode}-${width}.png`), fullPage: true});
        });
        await check('rendering makes no external request or browser error', async () => {
          assert.deepEqual(foreignRequests, []);
          assert.deepEqual(pageErrors, []);
          assert.deepEqual(await page.evaluate(() => [localStorage.length, sessionStorage.length, document.cookie]), [0, 0, '']);
        });
        await context.close();
      }
    } finally { await browser.close(); }
  }
} finally { await new Promise(resolve => server.close(resolve)); }
await writeFile(path.join(output, 'results.json'), JSON.stringify({results, passed: results.filter(value => value.passed).length, total: results.length}, null, 2) + '\n');
console.log(JSON.stringify({output, passed: results.filter(value => value.passed).length, total: results.length}));
if (failures.length) {
  console.error(failures.join('\n'));
  process.exitCode = 1;
}
