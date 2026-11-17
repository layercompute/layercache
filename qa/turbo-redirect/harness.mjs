// Compatibility check: fake Turbo remote cache + fake presigned object store,
// drives the real turbo binaries through redirect scenarios.
// usage: node harness.mjs <scratch-dir> [version ...]
import http from 'node:http'
import https from 'node:https'
import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import crypto from 'node:crypto'
import { spawn, execFileSync } from 'node:child_process'

const S = process.argv[2]
const versions = process.argv.slice(3).length ? process.argv.slice(3) : ['2.9.14', '2.10.12']
const OUT = path.join(S, 'out')
fs.rmSync(OUT, { recursive: true, force: true })
fs.mkdirSync(OUT, { recursive: true })

// ---------- shared state ----------
let mode = {}
let current = ''
let log = []
const seed = new Map() // hash -> { body, tag, dur, nonce }
const lastPut = new Map()
const tampered = (s) => {
  const raw = zlib.zstdDecompressSync(s.body)
  const out = Buffer.from(raw)
  const i = out.indexOf(s.nonce)
  if (i < 0) throw new Error('nonce not in tar')
  out.fill('X', i, i + s.nonce.length)
  return zlib.zstdCompressSync(out)
}
const pick = (h) => {
  const o = {}
  for (const k of Object.keys(h)) if (/^(x-artifact|x-vercel|content-type|content-length|user-agent|accept|expect|range)/i.test(k)) o[k] = h[k]
  return o
}
const rec = (srv, req, status, extra = {}) => log.push({ srv, method: req.method, url: req.url, auth: !!req.headers.authorization, authValue: req.headers.authorization ? req.headers.authorization.slice(0, 16) : undefined, status, headers: pick(req.headers), ...extra })
const drain = (req) => new Promise((res) => { const c = []; req.on('data', (d) => c.push(d)); req.on('end', () => res(Buffer.concat(c))); req.on('error', () => res(Buffer.concat(c))) })
const hashOf = (u) => (u.match(/\/v8\/artifacts\/([0-9a-f]+)/) || u.match(/\/bucket\/([0-9a-f]+)/) || [])[1]

let gwBase, objBase, closedPort
const targetUrl = (hash) => `${objBase}/bucket/${hash}?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=300&X-Amz-Signature=deadbeef`

function gateway(req, res) {
  const u = new URL(req.url, 'http://x')
  if (u.pathname === '/v8/artifacts/status') { rec('gw', req, 200); res.writeHead(200, { 'content-type': 'application/json' }); return res.end('{"status":"enabled"}') }
  if (u.pathname === '/v8/artifacts/events') { return drain(req).then(() => { rec('gw', req, 200); res.writeHead(200); res.end('{}') }) }
  const hash = hashOf(u.pathname)
  if (!hash) { rec('gw', req, 404); res.writeHead(404); return res.end() }
  const s = seed.get(current + hash)
  const firstHdrs = {}
  if (s) { firstHdrs['x-artifact-duration'] = String(s.dur); if (s.tag && mode.tagOnFirst !== false) firstHdrs['x-artifact-tag'] = s.tag }

  if (req.method === 'OPTIONS') {
    const h = { 'access-control-allow-origin': '*', 'access-control-allow-methods': 'GET,HEAD,PUT,OPTIONS' }
    const m = mode.options || 'none'
    if (m === 'location-noauth' || m === 'location-auth') h.location = targetUrl(hash)
    h['access-control-allow-headers'] = m === 'location-noauth' ? 'Content-Type, User-Agent, x-artifact-duration, x-artifact-tag' : 'Authorization, Content-Type, User-Agent, x-artifact-duration, x-artifact-tag'
    rec('gw', req, 200, { acrm: req.headers['access-control-request-method'] })
    res.writeHead(200, h)
    return res.end()
  }
  const redirect = (kind, loc, extra = {}) => { const code = Number(kind.slice(1)); rec('gw', req, code, extra); res.writeHead(code, { location: loc, ...firstHdrs, 'content-length': '0' }); res.end() }

  if (req.method === 'PUT') {
    return drain(req).then((b) => {
      const m = mode.put || 'store'
      if (m === 'store') {
        lastPut.set(hash, { body: b, headers: pick(req.headers) })
        rec('gw', req, 200, { bytes: b.length }); res.writeHead(200, { 'content-type': 'application/json' }); return res.end('{"urls":[]}')
      }
      if (m === 'status500') { rec('gw', req, 500, { bytes: b.length }); res.writeHead(500); return res.end() }
      redirect(m, targetUrl(hash), { bytes: b.length })
    })
  }
  if (req.method === 'HEAD') {
    const m = mode.head || 'proxy'
    if (m === 'miss' || !s) { rec('gw', req, 404); res.writeHead(404); return res.end() }
    if (m === 'proxy') { rec('gw', req, 200); res.writeHead(200, { ...firstHdrs, 'content-length': String(s.body.length) }); return res.end() }
    return redirect(m, targetUrl(hash))
  }
  if (req.method === 'GET') {
    const m = mode.get || 'proxy'
    if (m === 'miss' || !s) { rec('gw', req, 404); res.writeHead(404); return res.end() }
    if (m === 'proxy') { rec('gw', req, 200, { bytes: s.body.length }); res.writeHead(200, { ...firstHdrs, 'content-type': 'application/octet-stream', 'content-length': String(s.body.length) }); return res.end(s.body) }
    if (m === 'loop') return redirect('r302', `${gwBase}/v8/artifacts/${hash}?loop=${Number(u.searchParams.get('loop') || 0) + 1}`)
    if (m === 'gw500') { rec('gw', req, 500); res.writeHead(500); return res.end() }
    const loc = mode.target === 'refused' ? `http://localhost:${closedPort}/bucket/${hash}?X-Amz-Signature=deadbeef` : targetUrl(hash)
    return redirect(m, loc)
  }
  rec('gw', req, 405); res.writeHead(405); res.end()
}

function object(req, res) {
  const u = new URL(req.url, 'http://x')
  const hash = hashOf(u.pathname)
  const s = seed.get(current + hash)
  const xml = (code, name) => { res.writeHead(code, { 'content-type': 'application/xml' }); res.end(`<?xml version="1.0"?><Error><Code>${name}</Code><Message>${name}</Message></Error>`) }
  if (req.headers.authorization) { rec('obj', req, 400, { rejected: 'authorization-present', slugAdded: u.searchParams.has('slug') }); return xml(400, 'InvalidArgument') }
  if (!u.searchParams.get('X-Amz-Signature')) { rec('obj', req, 403, { rejected: 'no-signature' }); return xml(403, 'SignatureDoesNotMatch') }
  if (req.method === 'PUT') {
    return drain(req).then((b) => { const ok = true; rec('obj', req, 200, { bytes: b.length, slugAdded: u.searchParams.has('slug'), sha: crypto.createHash('sha256').update(b).digest('hex').slice(0, 12) }); lastPut.set('obj:' + hash, b); res.writeHead(200); res.end(); void ok })
  }
  if (req.method === 'HEAD') {
    if (mode.headTarget !== 'ok') { rec('obj', req, 403, { rejected: 'head-on-get-presign' }); res.writeHead(403); return res.end() }
    rec('obj', req, 200); res.writeHead(200, { 'content-length': String(s?.body.length ?? 0) }); return res.end()
  }
  if (req.method !== 'GET') { rec('obj', req, 405); res.writeHead(405); return res.end() }
  const t = mode.target || 'ok'
  const hdrs = { 'content-type': 'application/octet-stream', etag: '"abc"' }
  if (mode.durOnTarget) hdrs['x-artifact-duration'] = String(mode.durOnTarget)
  if (mode.tagOnTarget && s?.tag) hdrs['x-artifact-tag'] = s.tag
  switch (t) {
    case 'ok': rec('obj', req, 200, { bytes: s.body.length, slugAdded: u.searchParams.has('slug') }); res.writeHead(200, { ...hdrs, 'content-length': String(s.body.length) }); return res.end(s.body)
    case '403': rec('obj', req, 403); return xml(403, 'AccessDenied')
    case '404': rec('obj', req, 404); return xml(404, 'NoSuchKey')
    case '500': rec('obj', req, 500); return xml(500, 'InternalError')
    case 'truncate': {
      rec('obj', req, 200, { truncatedTo: Math.floor(s.body.length / 2) })
      res.writeHead(200, { ...hdrs, 'content-length': String(s.body.length) })
      res.write(s.body.subarray(0, Math.floor(s.body.length / 2)), () => setTimeout(() => req.socket.destroy(), 50))
      return
    }
    case 'garbage': { const b = crypto.randomBytes(s.body.length); rec('obj', req, 200, { bytes: b.length }); res.writeHead(200, { ...hdrs, 'content-length': String(b.length) }); return res.end(b) }
    case 'tampered': { const b = tampered(s); rec('obj', req, 200, { bytes: b.length }); res.writeHead(200, { ...hdrs, 'content-length': String(b.length) }); return res.end(b) }
    case 'hang': rec('obj', req, 0, { hung: true }); return
    default: rec('obj', req, 500); res.writeHead(500); res.end()
  }
}

// ---------- plumbing ----------
const listen = (srv) => new Promise((r) => srv.listen(0, '0.0.0.0', () => r(srv.address().port)))
const certDir = path.join(S, 'tls')
fs.mkdirSync(certDir, { recursive: true })
const ossl = (...a) => execFileSync('openssl', a, { stdio: 'ignore', cwd: certDir })
ossl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=harness-ca', '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-keyout', 'ca.key', '-out', 'ca.pem')
ossl('req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', 'k.pem', '-out', 'leaf.csr')
fs.writeFileSync(path.join(certDir, 'ext'), 'subjectAltName=DNS:localhost,IP:127.0.0.1\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n')
ossl('x509', '-req', '-in', 'leaf.csr', '-CA', 'ca.pem', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '2', '-sha256', '-extfile', 'ext', '-out', 'c.pem')
const tls = { key: fs.readFileSync(path.join(certDir, 'k.pem')), cert: fs.readFileSync(path.join(certDir, 'c.pem')) }

const servers = {
  gw: http.createServer(gateway), obj: http.createServer(object),
  gwS: https.createServer(tls, gateway), objS: https.createServer(tls, object),
}
const ports = {}
for (const [k, v] of Object.entries(servers)) ports[k] = await listen(v)
const tmp = http.createServer(); closedPort = await listen(tmp); await new Promise((r) => tmp.close(r))

function useScheme(secure) {
  gwBase = secure ? `https://127.0.0.1:${ports.gwS}` : `http://127.0.0.1:${ports.gw}`
  objBase = secure ? `https://localhost:${ports.objS}` : `http://localhost:${ports.obj}`
}
useScheme(false)

function setupWorkspace(version, flavor) {
  const dir = path.join(S, 'ws', version, flavor)
  fs.rmSync(dir, { recursive: true, force: true })
  fs.mkdirSync(path.join(dir, 'packages/a'), { recursive: true })
  const w = (p, c) => fs.writeFileSync(path.join(dir, p), typeof c === 'string' ? c : JSON.stringify(c, null, 2))
  w('package.json', { name: 'r', version: '0.0.0', private: true, packageManager: 'npm@11.6.2', workspaces: ['packages/*'] })
  w('package-lock.json', { name: 'r', version: '0.0.0', lockfileVersion: 3, requires: true, packages: { '': { name: 'r', version: '0.0.0', workspaces: ['packages/*'] }, 'node_modules/a': { resolved: 'packages/a', link: true }, 'packages/a': { version: '0.0.0' } } })
  w('turbo.json', { $schema: 'https://turborepo.dev/schema.json', tasks: { build: { outputs: ['dist/**'] } }, ...(flavor === 'sig' ? { remoteCache: { signature: true } } : {}) })
  w('.gitignore', 'node_modules\n.turbo\ndist\n.marker\n')
  w('packages/a/package.json', { name: 'a', version: '0.0.0', scripts: { build: 'node build.js' } })
  w('packages/a/build.js', `const fs=require('fs'),c=require('crypto');fs.mkdirSync('dist',{recursive:true});fs.writeFileSync('dist/out.txt','nonce:'+c.randomBytes(16).toString('hex')+'\\n');fs.appendFileSync('../../.marker','ran\\n');console.log('BUILD-RAN');\n`)
  const g = (...a) => execFileSync('git', a, { cwd: dir, stdio: 'ignore', env: { ...process.env, GIT_AUTHOR_NAME: 'h', GIT_AUTHOR_EMAIL: 'h@example.invalid', GIT_COMMITTER_NAME: 'h', GIT_COMMITTER_EMAIL: 'h@example.invalid', GIT_CONFIG_GLOBAL: '/dev/null' } })
  g('init', '-q'); g('add', '-A'); g('commit', '-q', '-m', 'seed')
  return dir
}

function cleanLocal(dir) {
  for (const p of ['.turbo', 'node_modules/.cache', 'packages/a/.turbo', 'packages/a/dist', '.marker']) fs.rmSync(path.join(dir, p), { recursive: true, force: true })
}

const HOME = path.join(S, 'home')
fs.mkdirSync(HOME, { recursive: true })

function turbo(version, dir, args, env, timeoutMs = 120000) {
  const bin = path.join(S, 'bin', `turbo-${version}`)
  const base = {
    PATH: `${path.dirname(process.execPath)}:/usr/bin:/bin`, HOME, XDG_CONFIG_HOME: path.join(HOME, '.config'), XDG_DATA_HOME: path.join(HOME, '.local/share'), XDG_CACHE_HOME: path.join(HOME, '.cache'),
    NO_COLOR: '1', CI: '1', TURBO_TELEMETRY_DISABLED: '1', DO_NOT_TRACK: '1', TURBO_NO_UPDATE_NOTIFIER: '1', TURBO_DAEMON: 'false', TURBO_UI: 'false',
    TURBO_API: gwBase, TURBO_TEAM: 'harness', TURBO_TOKEN: 'harness-bearer-token',
  }
  const e = { ...base, ...env }
  for (const k of Object.keys(e)) if (e[k] === null) delete e[k]
  return new Promise((resolve) => {
    const t0 = Date.now()
    const c = spawn(bin, args, { cwd: dir, env: e })
    let out = '', err = ''
    c.stdout.on('data', (d) => (out += d)); c.stderr.on('data', (d) => (err += d))
    const k = setTimeout(() => c.kill('SIGKILL'), timeoutMs)
    c.on('close', (code, sig) => { clearTimeout(k); resolve({ code, sig, out, err, ms: Date.now() - t0 }) })
  })
}

const interesting = /(WARNING|ERROR|failed to|turborepo_cache::|ArtifactTag|InvalidTag|Too many redirect|too many redirect|timed out)/
const noise = /(No locally installed|cache hit|cache miss|turborepo_cache::async_cache: (start|finish)|turborepo_lib::shim|using team|using token|hashing|Found go|globalDeps|package_graph|Global hash|environment|Executing|turbo_json|daemon|visited|with_env|\bevent\b.*hash)/i

async function runScenario(v, flavor, name, m, opts = {}) {
  const dir = opts.dir || path.join(S, 'ws', v, flavor)
  const ctx = seed.get(`ctx:${v}:${flavor}`)
  cleanLocal(dir)
  current = `${v}/${flavor}/`
  mode = m; log = []
  useScheme(!!opts.secure)
  const envAdd = { ...(flavor === 'sig' ? { TURBO_REMOTE_CACHE_SIGNATURE_KEY: 'harness-signing-key-0123456789abcdef' } : {}), ...(opts.env || {}) }
  const args = opts.dry ? ['run', 'build', '--dry=json', ...(opts.quiet ? [] : ['-vv'])] : ['run', 'build', '--summarize', '--output-logs=full', ...(opts.quiet ? [] : ['-vv'])]
  const r = await turbo(v, dir, args, envAdd)
  const marker = fs.existsSync(path.join(dir, '.marker')) ? fs.readFileSync(path.join(dir, '.marker'), 'utf8').trim().split('\n').length : 0
  const outFile = path.join(dir, 'packages/a/dist/out.txt')
  const output = fs.existsSync(outFile) ? fs.readFileSync(outFile, 'utf8') : null
  let cache = null
  try {
    if (opts.dry) { const j = JSON.parse(r.out.slice(r.out.indexOf('{'))); cache = j.tasks[0].cache }
    else { const rd = path.join(dir, '.turbo/runs'); const f = fs.readdirSync(rd).filter((x) => x.endsWith('.json'))[0]; cache = JSON.parse(fs.readFileSync(path.join(rd, f), 'utf8')).tasks[0].cache }
  } catch (e) { cache = `unavailable: ${e.message}` }
  const reqs = log.filter((l) => !/\/v8\/artifacts\/(status|events)/.test(l.url))
  const expected = ctx ? ctx.nonceLine : null
  const res = {
    version: v, flavor, name, mode: m, exit: r.code, signal: r.sig, ms: r.ms, buildRan: marker,
    restored: output == null ? 'no-output' : output === expected ? 'seed-bytes' : /^X+\n/.test(output) ? 'TAMPERED' : 'different(rebuilt)',
    cache, reqs,
    stdoutTail: r.out.split('\n').filter((l) => /(Tasks:|Cached:|Time:|FULL TURBO|cache hit|cache miss|BUILD-RAN|WARNING|ERROR|failed|error)/i.test(l)).slice(-14),
    warn: r.err.split('\n').filter((l) => interesting.test(l) && !noise.test(l)).map((l) => l.replace(/^\d{4}-\d\d-\d\dT[0-9:.]+Z /, '')).slice(0, 12),
  }
  fs.writeFileSync(path.join(OUT, `${v}-${flavor}-${name}.stdout.txt`), r.out)
  fs.writeFileSync(path.join(OUT, `${v}-${flavor}-${name}.stderr.txt`), r.err)
  fs.appendFileSync(path.join(OUT, 'results.jsonl'), JSON.stringify(res) + '\n')
  const compact = reqs.map((q) => `${q.srv}:${q.method}${q.url.includes('bucket') ? '(target)' : ''}=${q.status}${q.auth ? '+AUTH' : ''}${q.rejected ? `!${q.rejected}` : ''}${q.slugAdded ? '+SLUG' : ''}${q.bytes !== undefined ? `[${q.bytes}b]` : ''}`).join(' ')
  console.log(`[${v}/${flavor}] ${name}: exit=${r.code}${r.sig ? ' sig=' + r.sig : ''} ms=${r.ms} buildRan=${marker} restored=${res.restored} cache=${JSON.stringify(cache)}\n    reqs: ${compact}\n    ${res.stdoutTail.join(' | ')}${res.warn.length ? '\n    warn: ' + res.warn.join('\n          ') : ''}`)
  return res
}

async function seedFlavor(v, flavor) {
  const dir = setupWorkspace(v, flavor)
  current = `${v}/${flavor}/`
  cleanLocal(dir); mode = { get: 'miss', put: 'store' }; log = []; useScheme(false)
  const r = await turbo(v, dir, ['run', 'build', '--summarize', '--output-logs=full'], flavor === 'sig' ? { TURBO_REMOTE_CACHE_SIGNATURE_KEY: 'harness-signing-key-0123456789abcdef' } : {})
  const put = log.find((l) => l.method === 'PUT')
  if (!put) { console.log(r.out, r.err, JSON.stringify(log, null, 1)); throw new Error('seed produced no PUT') }
  const hash = hashOf(put.url)
  const stored = lastPut.get(hash)
  const nonceLine = fs.readFileSync(path.join(dir, 'packages/a/dist/out.txt'), 'utf8')
  const dur = Number(stored.headers['x-artifact-duration'] || 0)
  const body = stored.body
  seed.set(`${v}/${flavor}/` + hash, { body, tag: stored.headers['x-artifact-tag'], dur: dur || 777, nonce: nonceLine.trim() })
  seed.set(`ctx:${v}:${flavor}`, { hash, nonceLine })
  console.log(`\n=== ${v}/${flavor} seed hash=${hash} bytes=${body.length} tag=${stored.headers['x-artifact-tag'] ? 'yes' : 'no'} putHeaders=${JSON.stringify(stored.headers)} query=${put.url}`)
}

const sharedConfigSetup = (saved) => {
  const d = path.join(HOME, '.config/turborepo')
  fs.mkdirSync(d, { recursive: true })
  if (saved) fs.writeFileSync(path.join(d, 'config.json'), JSON.stringify({ token: 'saved-config-token' }))
  else fs.rmSync(path.join(d, 'config.json'), { force: true })
}

for (const v of versions) {
  sharedConfigSetup(false)
  await seedFlavor(v, 'plain')
  await seedFlavor(v, 'sig')
  const P = (name, m, o) => runScenario(v, 'plain', name, m, o)
  const G = (name, m, o) => runScenario(v, 'sig', name, m, o)

  console.log(`\n--- GET ${v}`)
  await P('get-proxy-control', { get: 'proxy' })
  await P('get-miss-control', { get: 'miss' })
  await P('get-302-target-ok', { get: 'r302' })
  await P('get-307-target-ok', { get: 'r307' })
  await P('get-301-target-ok', { get: 'r301' })
  await P('get-303-target-ok', { get: 'r303' })
  await P('get-308-target-ok', { get: 'r308' })
  await P('get-302-duration-on-target', { get: 'r302', durOnTarget: 4242 })
  await P('get-302-https', { get: 'r302' }, { secure: true, env: { SSL_CERT_FILE: path.join(certDir, 'ca.pem') } })

  console.log(`\n--- target failure ${v}`)
  await P('fail-403-expired', { get: 'r302', target: '403' })
  await P('fail-404', { get: 'r302', target: '404' })
  await P('fail-500', { get: 'r302', target: '500' })
  await P('fail-refused', { get: 'r302', target: 'refused' })
  await P('fail-truncate', { get: 'r302', target: 'truncate' })
  await P('fail-garbage', { get: 'r302', target: 'garbage' })
  await P('fail-tampered-validtar', { get: 'r302', target: 'tampered' })
  await P('fail-redirect-loop', { get: 'loop' })
  await P('fail-hang-timeout3s', { get: 'r302', target: 'hang' }, { env: { TURBO_REMOTE_CACHE_TIMEOUT: '3' } })
  await P('control-gw500', { get: 'gw500' })

  console.log(`\n--- signature ${v}`)
  await G('sig-proxy-control', { get: 'proxy' })
  await G('sig-302-tag-first-only', { get: 'r302' })
  await G('sig-302-tag-on-target', { get: 'r302', tagOnTarget: true })
  await G('sig-302-tampered-tag-on-target', { get: 'r302', tagOnTarget: true, target: 'tampered' })

  console.log(`\n--- HEAD (dry-run) ${v}`)
  await P('head-proxy-control', { head: 'proxy' }, { dry: true })
  await P('head-miss-control', { head: 'miss' }, { dry: true })
  await P('head-302-target-ok', { head: 'r302', headTarget: 'ok' }, { dry: true })
  await P('head-302-target-403', { head: 'r302', headTarget: 'no' }, { dry: true })
  await P('head-200-get-302-realrun', { head: 'proxy', get: 'r302' })

  console.log(`\n--- PUT ${v}`)
  await P('put-200-control', { get: 'miss', put: 'store' })
  await P('put-307', { get: 'miss', put: 'r307' })
  await P('put-308', { get: 'miss', put: 'r308' })
  await P('put-302', { get: 'miss', put: 'r302' })
  await P('put-301', { get: 'miss', put: 'r301' })
  await P('put-303', { get: 'miss', put: 'r303' })
  await P('put-500-control', { get: 'miss', put: 'status500' })

  console.log(`\n--- quiet user-visible output ${v}`)
  await P('quiet-get-302', { get: 'r302' }, { quiet: true })
  await P('quiet-fail-403', { get: 'r302', target: '403' }, { quiet: true })
  await P('quiet-fail-garbage', { get: 'r302', target: 'garbage' }, { quiet: true })
  await G('quiet-sig-302-tag-first-only', { get: 'r302' }, { quiet: true })
  await P('quiet-put-307', { get: 'miss', put: 'r307' }, { quiet: true })

  console.log(`\n--- preflight / saved token ${v}`)
  await P('preflight-get-location-noauth', { get: 'proxy', options: 'location-noauth' }, { env: { TURBO_PREFLIGHT: 'true' } })
  await P('preflight-get-location-auth', { get: 'proxy', options: 'location-auth' }, { env: { TURBO_PREFLIGHT: 'true' } })
  await P('preflight-put-location-noauth', { target: '404', options: 'location-noauth' }, { env: { TURBO_PREFLIGHT: 'true' } })
  sharedConfigSetup(true)
  await P('saved-config-token-302', { get: 'r302' }, { env: { TURBO_TOKEN: null } })
  sharedConfigSetup(false)
}

for (const s of Object.values(servers)) { s.closeAllConnections(); s.close() }
console.log('\nDONE')
