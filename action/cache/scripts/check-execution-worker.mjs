import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const directory = await mkdtemp(join(tmpdir(), "layercache-cache-worker-"));
const unexpectedRequests = [];
let oidcRequests = 0;
const server = createServer((request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname !== "/oidc" || decodeURIComponent(url.searchParams.get("audience") ?? "") !== "layercache:github.com/acme/widget") {
    unexpectedRequests.push(url.pathname);
    response.writeHead(500).end();
    return;
  }
  oidcRequests++;
  response.writeHead(200, { "Content-Type": "application/json" });
  response.end(JSON.stringify({ value: "fixture-github-oidc-token" }));
});

try {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const endpoint = `http://127.0.0.1:${server.address().port}`;
  const preload = join(directory, "preload.mjs");
  await writeFile(preload, `
    import assert from 'node:assert/strict';
    import { writeFileSync } from 'node:fs';
    globalThis.fetch = async (input, request) => {
      try {
        const url = new URL(input);
        assert.equal(url.origin, process.env.INPUT_ENDPOINT);
        if (url.pathname === '/v1/auth/github-oidc/exchange') {
          return Response.json({
            teamToken: 'fixture-team-token',
            expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
            executionObservations: { version: 1, path: '/v1/executions/observations' },
          });
        }
        assert.equal(url.pathname, '/v1/executions/observations');
        assert.equal(request.redirect, 'error');
        assert.equal(new Headers(request.headers).get('Authorization'), 'Bearer fixture-team-token');
        assert.equal(new Headers(request.headers).get('X-LayerCache-Project'), 'github.com/acme/widget');
        const source = String(request.body);
        assert.ok(Buffer.byteLength(source) <= 16 * 1024);
        for (const value of [process.env.GITHUB_WORKSPACE, 'fixture-team-token', 'fixture-environment-secret']) {
          assert.ok(!source.includes(value));
        }
        const payload = JSON.parse(source);
        assert.equal(payload.phase, process.env.FIXTURE_EXPECTED_PHASE);
        assert.equal(payload.provider, 'ubicloud');
        assert.ok(['linux', 'darwin', 'windows', 'other'].includes(payload.sample.os));
        assert.equal(payload.sample.cpu.effectiveMilliCPU, null);
        assert.equal(payload.sample.memory.effectiveLimitBytes, null);
        assert.equal(payload.sample.filesystems.length, 1);
        assert.deepEqual(payload.sample.filesystems[0].roles, ['workspace', 'staging']);
        writeFileSync(process.env.FIXTURE_PROOF, source);
        process.exit(0);
      } catch (error) {
        process.stderr.write(String(error));
        process.exit(9);
      }
    };
  `);

  for (const [entry, phase] of [["main", "start"], ["post", "finish"]]) {
    const proof = join(directory, `${entry}.json`);
    const child = spawn(process.execPath, ["--import", preload, join(root, "dist", entry, "index.js")], {
      cwd: directory,
      env: {
        INPUT_ENDPOINT: endpoint,
        INPUT_PROJECT: "github.com/acme/widget",
        INPUT_PATH: "node_modules",
        INPUT_KEY: "fixture-key",
        "INPUT_LOOKUP-ONLY": "false",
        "INPUT_FAIL-ON-CACHE-MISS": "false",
        "INPUT_EXECUTION-PROVIDER": "ubicloud",
        ACTIONS_ID_TOKEN_REQUEST_URL: `${endpoint}/oidc?request=fixture`,
        ACTIONS_ID_TOKEN_REQUEST_TOKEN: "fixture-request-token",
        GITHUB_WORKSPACE: directory,
        RUNNER_TEMP: directory,
        "STATE_cache-primary-key": "fixture-key",
        "STATE_cache-paths": '["node_modules"]',
        FIXTURE_EXPECTED_PHASE: phase,
        FIXTURE_PROOF: proof,
        PRIVATE_ENVIRONMENT: "fixture-environment-secret",
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    let output = "";
    child.stdout.on("data", (chunk) => { output += chunk; });
    child.stderr.on("data", (chunk) => { output += chunk; });
    const code = await new Promise((resolve, reject) => {
      const deadline = setTimeout(() => { child.kill("SIGKILL"); }, 6000);
      child.once("error", (error) => { clearTimeout(deadline); reject(error); });
      child.once("close", (code) => { clearTimeout(deadline); resolve(code); });
    });
    assert.equal(code, 0, `${entry} bundle worker failed: ${output.slice(-2000)}`);
    const source = await readFile(proof, "utf8").catch(() => {
      throw new Error(`${entry} bundle did not emit its worker sample: ${output.slice(-2000)}`);
    });
    const payload = JSON.parse(source);
    assert.equal(payload.phase, phase);
  }
  assert.equal(oidcRequests, 2);
  assert.deepEqual(unexpectedRequests, []);
  process.stdout.write("Verified main and post bundles load their isolated observation workers.\n");
} finally {
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
  await rm(directory, { recursive: true, force: true });
}
