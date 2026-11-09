import { createHash, generateKeyPairSync, sign } from "node:crypto";
import { createServer, type IncomingHttpHeaders, type ServerResponse } from "node:http";
import { promises as fs } from "node:fs";
import {
  chmod as fsChmod,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  readlink as fsReadlink,
  rm,
  stat as fsStat,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Writable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { inspect } from "node:util";
import { createGzip } from "node:zlib";

import * as tar from "tar-stream";
import { describe, expect, it, vi } from "vitest";

import { VerifiedCacheClient, type VerifiedCacheIdentity } from "../src/verified-cache.js";

const payloadType = "application/vnd.in-toto+json";

describe("verified Public Cache client", () => {
  it("verifies, validates, and extracts a signed archive through the v1 protocol", async () => {
    const workspace = await mkdtemp(join(tmpdir(), "layercache-workspace-"));
    const archive = await gzipArchive("node_modules/widget/index.js", "restored safely");
    let signedArchive = archive;
    let servedArchive = archive;
    let stallArchive = false;
    let lookupDelayMilliseconds = 0;
    let publicationCompatibility = "linux-amd64-schema1";
    let publicationInputs: Array<{ name: string; value: string }> | undefined;
    let publicationBuilderImageDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc";
    const { publicKey, privateKey } = generateKeyPairSync("ed25519");
    const trustKey = publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64url");
    const identity: VerifiedCacheIdentity = {
      repository: "acme/widgets",
      ref: "refs/heads/main",
      commit: "0123456789abcdef0123456789abcdef01234567",
      compatibility: "linux-amd64-schema1",
      recipeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      target: ".github/workflows/public-cache.yml#public-cache",
      platform: "linux/amd64",
      inputs: [{ name: "compatibility", value: "linux-amd64-schema1" }],
      toolchain: "actions/cache@6.2.0",
      builder: "layercache-public-builder@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    };
    const primaryKey = "pnpm-linux";
    let endpoint = "";
    const server = createServer(async (request, response) => {
      const target = new URL(request.url ?? "/", endpoint);
      if (target.pathname.endsWith("/_apis/artifactcache/cache")) {
        if (lookupDelayMilliseconds !== 0) {
          await new Promise((resolvePromise) => setTimeout(resolvePromise, lookupDelayMilliseconds));
          lookupDelayMilliseconds = 0;
        }
        const digest = createHash("sha256").update(signedArchive).digest("hex");
        const version = target.searchParams.get("version") ?? "";
        const exactInputs = actionsPublicInputs(identity, primaryKey, version);
        const nativeKey = actionsPublicNativeKey(identity, primaryKey, version);
        const publication = {
          integration: "actions",
          project: identity.repository,
          compatibility: publicationCompatibility,
          nativeKey,
          repository: `https://github.com/${identity.repository}`,
          commit: identity.commit,
          recipeDigest: identity.recipeDigest,
          target: identity.target,
          platform: identity.platform,
          inputs: publicationInputs ?? exactInputs,
          toolchain: identity.toolchain,
          builder: identity.builder,
          builderImageDigest: publicationBuilderImageDigest,
          digest,
          size: signedArchive.length,
          durationMs: 1,
          buildId: "build-123",
          issuedAt: new Date(Date.now() - 60_000).toISOString(),
          expiresAt: new Date(Date.now() + 60 * 60_000).toISOString(),
        };
        const publicIdentity = publicationIdentity(publication);
        const payload = Buffer.from(JSON.stringify({
          _type: "https://in-toto.io/Statement/v1",
          subject: [{ name: `layercache-public:${publicIdentity}`, digest: { sha256: digest } }],
          predicateType: "https://layercache.dev/attestation/public-cache/v1",
          predicate: publication,
        }));
        const signature = sign(null, pae(payloadType, payload), privateKey).toString("base64");
        const cacheIdentity = createHash("sha256").update([
          "actions", identity.repository, identity.compatibility, nativeKey,
        ].join("\0")).digest("hex");
        response.setHeader("Content-Type", "application/json");
        response.end(JSON.stringify({
          cacheKey: primaryKey,
          cacheVersion: version,
          archiveLocation: new URL("archive", endpoint).toString(),
          layerCacheOrigin: "publicCache",
          layerCachePublic: {
            request: {
              cacheIdentity,
              expected: {
                Integration: "actions",
                Project: identity.repository,
                Compatibility: publicationCompatibility,
                NativeKey: nativeKey,
                Target: identity.target,
                Inputs: exactInputs,
              },
              repository: identity.repository,
              sourceRepository: `https://github.com/${identity.repository}`,
              ref: identity.ref,
              key: primaryKey,
              version,
              compatibility: identity.compatibility,
              sourceCommit: identity.commit,
              recipeDigest: identity.recipeDigest,
              target: identity.target,
              platform: identity.platform,
              toolchain: identity.toolchain,
              builder: identity.builder,
            },
            envelope: { payloadType, payload: payload.toString("base64"), signatures: [{ keyid: "test", sig: signature }] },
            digest,
            size: signedArchive.length,
            expiresAt: publication.expiresAt,
            publicIdentity,
          },
        }));
        return;
      }
      if (target.pathname.endsWith("/archive")) {
        response.setHeader("Content-Type", "application/octet-stream");
        if (stallArchive) {
          response.write(servedArchive.subarray(0, 1));
          setTimeout(() => response.end(servedArchive.subarray(1)), 200);
        } else {
          response.end(servedArchive);
        }
        return;
      }
      response.statusCode = 404;
      response.end();
    });

    try {
      await new Promise<void>((resolvePromise) => server.listen(0, "127.0.0.1", resolvePromise));
      const address = server.address();
      if (address === null || typeof address === "string") {
        throw new Error("test server did not expose a TCP address");
      }
      endpoint = `http://127.0.0.1:${address.port}/v1/_layercache/compatibility/${identity.compatibility}/`;
      await mkdir(join(workspace, "node_modules/widget"), { recursive: true });
      await writeFile(join(workspace, "node_modules/widget/index.js"), "stale contents");
      await writeFile(join(workspace, "unrelated.txt"), "preserve me");
      const client = new VerifiedCacheClient({
        env: {
          ACTIONS_CACHE_URL: endpoint,
          ACTIONS_RUNTIME_TOKEN: "team-token",
          GITHUB_WORKSPACE: workspace,
        },
        trustKey,
        identity,
      });

      await expect(client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
      await expect(readFile(join(workspace, "node_modules/widget/index.js"), "utf8")).resolves.toBe("restored safely");
      await expect(readFile(join(workspace, "unrelated.txt"), "utf8")).resolves.toBe("preserve me");

      signedArchive = await gzipReadOnlyDirectoryArchive();
      servedArchive = signedArchive;
      await expect(client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
      await expect(readFile(join(workspace, "node_modules/readonly/data.txt"), "utf8")).resolves.toBe("read only");
      expect((await fsStat(join(workspace, "node_modules/readonly"))).mode & 0o777).toBe(0o555);

      signedArchive = await gzipSymlinkArchive();
      servedArchive = signedArchive;
      await expect(client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
      await expect(fsReadlink(join(workspace, "node_modules/current.js"))).resolves.toBe("widget/index.js");

      signedArchive = await gzipHardlinkArchive();
      servedArchive = signedArchive;
      await expect(client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
      const hardlinkTarget = await fsStat(join(workspace, "node_modules/widget/index.js"));
      const hardlink = await fsStat(join(workspace, "node_modules/current-hardlink.js"));
      expect([hardlink.dev, hardlink.ino]).toEqual([hardlinkTarget.dev, hardlinkTarget.ino]);

      const rollbackParent = join(workspace, "node_modules/rollback-parent");
      const rollbackChild = join(rollbackParent, "child");
      await mkdir(join(rollbackChild, "conflict"), { recursive: true });
      await fsChmod(rollbackChild, 0o300);
      await fsChmod(rollbackParent, 0o100);
      signedArchive = await gzipRollbackConflictArchive();
      servedArchive = signedArchive;
      const chmod = vi.spyOn(fs, "chmod");
      try {
        await expect(client.restoreCache(["node_modules"], primaryKey))
          .rejects.toThrow("would replace an existing directory");
        const restoredDirectories = chmod.mock.calls
          .filter(([path, mode]) =>
            path === rollbackChild && mode === 0o300 || path === rollbackParent && mode === 0o100)
          .map(([path]) => path);
        expect(restoredDirectories).toEqual([rollbackChild, rollbackParent]);
      } finally {
        chmod.mockRestore();
        await fsChmod(rollbackChild, 0o700);
        await fsChmod(rollbackParent, 0o700);
      }

      signedArchive = archive;
      servedArchive = archive;

      lookupDelayMilliseconds = 100;
      await expect(client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);

      publicationCompatibility = "darwin-arm64-schema1";
      await expect(client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("expected Actions identity");
      publicationCompatibility = identity.compatibility;

      publicationInputs = [{ name: "compatibility", value: "linux-amd64-schema2" }];
      await expect(client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("expected Actions identity");
      publicationInputs = undefined;

      publicationBuilderImageDigest = "sha256:invalid";
      await expect(client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("output identity");
      publicationBuilderImageDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc";

      await writeFile(join(workspace, "node_modules/widget/index.js"), "sentinel after success");
      servedArchive = Buffer.from(archive);
      servedArchive[servedArchive.length - 1] = (servedArchive.at(-1) ?? 0) ^ 0xff;
      await expect(client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("signed publication");
      await expect(readFile(join(workspace, "node_modules/widget/index.js"), "utf8"))
        .resolves.toBe("sentinel after success");

      signedArchive = await gzipArchive("../layercache-escape.txt", "must not escape");
      servedArchive = signedArchive;
      await expect(client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("escapes the workspace");
      await expect(readFile(join(workspace, "node_modules/widget/index.js"), "utf8"))
        .resolves.toBe("sentinel after success");

      signedArchive = archive;
      servedArchive = archive;
      stallArchive = true;
      await expect(client.restoreCache(["node_modules"], primaryKey, [], { segmentTimeoutInMs: 50 }))
        .rejects.toThrow("made no progress");
      await expect(readFile(join(workspace, "node_modules/widget/index.js"), "utf8"))
        .resolves.toBe("sentinel after success");
      expect((await readdir(workspace)).filter((name) => name.startsWith(".layercache-restore-"))).toEqual([]);
    } finally {
      await new Promise<void>((resolvePromise, reject) => server.close((error) => error ? reject(error) : resolvePromise()));
      await fsChmod(join(workspace, "node_modules/readonly"), 0o755).catch(() => undefined);
      await rm(workspace, { recursive: true, force: true });
    }
  });
});

async function gzipArchive(name: string, contents: string): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const pack = tar.pack();
  const completed = pipeline(pack, createGzip(), new Writable({
    write(chunk: Buffer, _encoding, callback) {
      chunks.push(Buffer.from(chunk));
      callback();
    },
  }));
  pack.entry({ name, mode: 0o644 }, contents);
  pack.finalize();
  await completed;
  return Buffer.concat(chunks);
}

async function gzipReadOnlyDirectoryArchive(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const pack = tar.pack();
  const completed = pipeline(pack, createGzip(), new Writable({
    write(chunk: Buffer, _encoding, callback) {
      chunks.push(Buffer.from(chunk));
      callback();
    },
  }));
  pack.entry({ name: "node_modules/readonly", type: "directory", mode: 0o555 });
  pack.entry({ name: "node_modules/readonly/data.txt", mode: 0o444 }, "read only");
  pack.finalize();
  await completed;
  return Buffer.concat(chunks);
}

async function gzipSymlinkArchive(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const pack = tar.pack();
  const completed = pipeline(pack, createGzip(), new Writable({
    write(chunk: Buffer, _encoding, callback) {
      chunks.push(Buffer.from(chunk));
      callback();
    },
  }));
  pack.entry({ name: "node_modules/widget/index.js", mode: 0o644 }, "linked contents");
  pack.entry({
    name: "node_modules/current.js",
    type: "symlink",
    linkname: "widget/index.js",
  });
  pack.finalize();
  await completed;
  return Buffer.concat(chunks);
}

async function gzipHardlinkArchive(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const pack = tar.pack();
  const completed = pipeline(pack, createGzip(), new Writable({
    write(chunk: Buffer, _encoding, callback) {
      chunks.push(Buffer.from(chunk));
      callback();
    },
  }));
  pack.entry({ name: "node_modules/widget/index.js", mode: 0o644 }, "linked contents");
  pack.entry({
    name: "node_modules/current-hardlink.js",
    type: "link",
    linkname: "node_modules/widget/index.js",
  });
  pack.finalize();
  await completed;
  return Buffer.concat(chunks);
}

async function gzipRollbackConflictArchive(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const pack = tar.pack();
  const completed = pipeline(pack, createGzip(), new Writable({
    write(chunk: Buffer, _encoding, callback) {
      chunks.push(Buffer.from(chunk));
      callback();
    },
  }));
  pack.entry({ name: "node_modules/rollback-parent", type: "directory", mode: 0o755 });
  pack.entry({ name: "node_modules/rollback-parent/child", type: "directory", mode: 0o755 });
  pack.entry({ name: "node_modules/rollback-parent/child/conflict", mode: 0o644 }, "replacement");
  pack.finalize();
  await completed;
  return Buffer.concat(chunks);
}

function actionsPublicNativeKey(identity: VerifiedCacheIdentity, key: string, version: string): string {
  const hasher = createHash("sha256");
  for (const value of [
    "layercache/actions-cache/public-identity/v1", identity.repository, identity.ref, key, version,
    identity.compatibility, identity.commit, identity.recipeDigest, identity.platform, identity.toolchain,
    identity.builder,
  ]) {
    const length = Buffer.alloc(8);
    length.writeBigUInt64BE(BigInt(Buffer.byteLength(value)));
    hasher.update(length);
    hasher.update(value);
  }
  return `sha256:${hasher.digest("hex")}`;
}

function actionsPublicInputs(identity: VerifiedCacheIdentity, key: string, version: string) {
  return [
    { name: "actions.key", value: key },
    { name: "actions.ref", value: identity.ref },
    { name: "actions.version", value: version },
    { name: "compatibility", value: identity.compatibility },
  ];
}

function publicationIdentity(
  publication: Record<string, string | number | Array<{ name: string; value: string }>>,
): string {
  const inputs = publication.inputs as Array<{ name: string; value: string }>;
  const parts = [
    publication.integration, publication.project, publication.compatibility, publication.nativeKey,
    publication.repository, publication.commit, publication.recipeDigest, publication.target,
    publication.platform,
  ];
  for (const input of inputs) {
    parts.push(input.name, input.value);
  }
  parts.push(publication.toolchain, publication.builder, publication.builderImageDigest, publication.buildId,
    publication.digest, String(publication.size),
  );
  return createHash("sha256").update(parts.join("\0")).digest("hex");
}

function pae(type: string, payload: Buffer): Buffer {
  return Buffer.concat([Buffer.from(`DSSEv1 ${Buffer.byteLength(type)} ${type} ${payload.length} `), payload]);
}

describe("direct object transfer", () => {
  const secret = "presigned-secret-signature";
  const query = `X-Amz-Signature=${secret}`;
  const mediaType = "application/vnd.layercache.transfer.v1+json";
  const primaryKey = "pnpm-linux";
  const identity: VerifiedCacheIdentity = {
    repository: "acme/widgets",
    ref: "refs/heads/main",
    commit: "0123456789abcdef0123456789abcdef01234567",
    compatibility: "linux-amd64-schema1",
    recipeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    target: ".github/workflows/public-cache.yml#public-cache",
    platform: "linux/amd64",
    inputs: [{ name: "compatibility", value: "linux-amd64-schema1" }],
    toolchain: "actions/cache@6.2.0",
    builder: "layercache-public-builder@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  };

  interface Seen {
    method: string;
    path: string;
    headers: IncomingHttpHeaders;
    body: string;
    archiveDirectoryFiles?: string[];
  }
  interface Scenario {
    transfer?: (base: Record<string, unknown>) => unknown;
    storage?: (response: ServerResponse) => void;
    complete?: (response: ServerResponse) => void;
    archive?: (response: ServerResponse) => void;
    publicMetadata?: (version: string, digest: string, size: number) => Record<string, unknown>;
  }

  const sha = (bytes: Buffer) => createHash("sha256").update(bytes).digest("hex");

  async function fixture() {
    const scratch = await mkdtemp(join(tmpdir(), "layercache-direct-scratch-"));
    const workspace = join(scratch, "workspace");
    const temporaries = join(scratch, "tmp");
    await mkdir(workspace);
    await mkdir(temporaries);
    const previousTemporary = process.env.TMPDIR;
    process.env.TMPDIR = temporaries;
    const directBytes = await gzipArchive("node_modules/widget/index.js", "from storage");
    const proxiedBytes = await gzipArchive("node_modules/widget/index.js", "from archive location");
    const { publicKey, privateKey } = generateKeyPairSync("ed25519");
    const trustKey = publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64url");
    const seen: Seen[] = [];
    const scenario: Scenario = {};
    let endpoint = "";
    const archiveFiles = async () => {
      const files: string[] = [];
      for (const entry of await readdir(temporaries)) {
        files.push(...await readdir(join(temporaries, entry)));
      }
      return files;
    };
    const server = createServer(async (incoming, response) => {
      const chunks: Buffer[] = [];
      for await (const chunk of incoming) {
        chunks.push(chunk as Buffer);
      }
      const target = new URL(incoming.url ?? "/", endpoint);
      const request: Seen = {
        method: incoming.method ?? "",
        path: target.pathname,
        headers: incoming.headers,
        body: Buffer.concat(chunks).toString("utf8"),
      };
      seen.push(request);
      if (target.pathname.endsWith("/_apis/artifactcache/cache")) {
        const version = target.searchParams.get("version") ?? "";
        const base = {
          transfer: "direct-v1",
          method: "GET",
          url: new URL(`/storage/object?${query}`, endpoint).toString(),
          digest: `sha256:${sha(directBytes)}`,
          size: directBytes.length,
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
          lease: "lease-1",
        };
        const lookup = {
          cacheKey: primaryKey,
          cacheVersion: version,
          archiveLocation: new URL("archive?outcome=signed", endpoint).toString(),
          layerCacheTransfer: scenario.transfer === undefined ? base : scenario.transfer(base),
          ...(scenario.publicMetadata === undefined ? {} : {
            layerCacheOrigin: "publicCache",
            layerCachePublic: scenario.publicMetadata(version, sha(proxiedBytes), proxiedBytes.length),
          }),
        };
        response.setHeader("Content-Type", "application/json");
        response.end(JSON.stringify(lookup));
        return;
      }
      if (target.pathname === "/storage/object") {
        if (scenario.storage !== undefined) {
          scenario.storage(response);
          return;
        }
        response.end(directBytes);
        return;
      }
      if (target.pathname.endsWith("/archive") && request.method === "POST") {
        if (scenario.complete !== undefined) {
          scenario.complete(response);
          return;
        }
        response.statusCode = 204;
        response.end();
        return;
      }
      if (target.pathname.endsWith("/archive")) {
        request.archiveDirectoryFiles = await archiveFiles();
        if (scenario.archive !== undefined) {
          scenario.archive(response);
          return;
        }
        response.end(proxiedBytes);
        return;
      }
      response.statusCode = 404;
      response.end();
    });
    await new Promise<void>((resolvePromise) => server.listen(0, "127.0.0.1", resolvePromise));
    const address = server.address();
    if (address === null || typeof address === "string") {
      throw new Error("test server did not expose a TCP address");
    }
    endpoint = `http://127.0.0.1:${address.port}/v1/_layercache/compatibility/${identity.compatibility}/`;
    const client = new VerifiedCacheClient({
      env: { ACTIONS_CACHE_URL: endpoint, ACTIONS_RUNTIME_TOKEN: "team-token", GITHUB_WORKSPACE: workspace },
      trustKey,
      identity,
    });
    return {
      client, workspace, directBytes, proxiedBytes, privateKey, seen, scenario, endpoint,
      restored: () => readFile(join(workspace, "node_modules/widget/index.js"), "utf8"),
      lookups: () => seen.filter((request) => request.path.endsWith("/_apis/artifactcache/cache")),
      storageReads: () => seen.filter((request) => request.path === "/storage/object"),
      archiveReads: () => seen.filter((request) => request.path.endsWith("/archive") && request.method === "GET"),
      completions: () => seen.filter((request) => request.path.endsWith("/archive") && request.method === "POST"),
      cleanup: async () => {
        server.closeAllConnections();
        await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()));
        if (previousTemporary === undefined) {
          delete process.env.TMPDIR;
        } else {
          process.env.TMPDIR = previousTemporary;
        }
        await rm(scratch, { recursive: true, force: true });
      },
    };
  }
  type Fixture = Awaited<ReturnType<typeof fixture>>;

  async function withFixture(run: (context: Fixture) => Promise<void>): Promise<void> {
    const context = await fixture();
    try {
      await run(context);
    } finally {
      await context.cleanup();
    }
  }

  it("reads directly with no credentials, verifies, extracts and completes once", () => withFixture(async (f) => {
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
    await expect(f.restored()).resolves.toBe("from storage");
    expect(f.lookups()[0]?.headers.accept).toContain(mediaType);
    expect(f.storageReads()).toHaveLength(1);
    expect(f.storageReads()[0]?.headers.authorization).toBeUndefined();
    expect(f.archiveReads()).toHaveLength(0);
    expect(f.completions()).toHaveLength(1);
    expect(f.completions()[0]?.headers.authorization).toBe("Bearer team-token");
    expect(JSON.parse(f.completions()[0]?.body ?? "")).toEqual({
      lease: "lease-1", digest: `sha256:${sha(f.directBytes)}`, size: f.directBytes.length,
    });
  }));

  it("sends no opt-in for a lookup-only probe", () => withFixture(async (f) => {
    await expect(f.client.restoreCache(["node_modules"], primaryKey, [], { lookupOnly: true })).resolves.toBe(primaryKey);
    expect(f.lookups()[0]?.headers.accept).not.toContain(mediaType);
    expect(f.storageReads()).toHaveLength(0);
    expect(f.completions()).toHaveLength(0);
  }));

  const failures: Array<[string, (f: Fixture) => void]> = [
    ["storage answers 403", (f) => { f.scenario.storage = (response) => { response.statusCode = 403; response.end(); }; }],
    ["the connection drops", (f) => { f.scenario.storage = (response) => { response.destroy(); }; }],
    ["a redirect is refused", (f) => { f.scenario.storage = (response) => {
      response.writeHead(302, { Location: new URL("/storage/followed", f.endpoint).toString() });
      response.end();
    }; }],
    ["wrong digest at the right size", (f) => { f.scenario.storage = (response) => { response.end(Buffer.alloc(f.directBytes.length)); }; }],
    ["a clean truncation", (f) => { f.scenario.storage = (response) => { response.end(f.directBytes.subarray(0, 8)); }; }],
    ["truncated under a declared length", (f) => { f.scenario.storage = (response) => {
      response.writeHead(200, { "Content-Length": f.directBytes.length });
      response.write(f.directBytes.subarray(0, 8));
      setImmediate(() => response.destroy());
    }; }],
    ["oversize", (f) => { f.scenario.storage = (response) => { response.write(Buffer.alloc(f.directBytes.length + 1)); response.end(); }; }],
    ["wrong declared size", (f) => { f.scenario.transfer = (base) => ({ ...base, size: f.directBytes.length + 1 }); }],
    ["expired descriptor", (f) => { f.scenario.transfer = (base) => ({ ...base, expiresAt: new Date(Date.now() - 1).toISOString() }); }],
    ["invalid digest", (f) => { f.scenario.transfer = (base) => ({ ...base, digest: "sha256:abc" }); }],
    ["zero size", (f) => { f.scenario.transfer = (base) => ({ ...base, size: 0 }); }],
    ["size above the archive limit", (f) => { f.scenario.transfer = (base) => ({ ...base, size: 10 * 1024 ** 3 + 1 }); }],
    ["empty lease", (f) => { f.scenario.transfer = (base) => ({ ...base, lease: "" }); }],
    ["plain http off loopback", (f) => { f.scenario.transfer = (base) => ({ ...base, url: `http://example.com/object?${query}` }); }],
    ["userinfo in the URL", (f) => { f.scenario.transfer = (base) => ({ ...base, url: `https://user:pass@example.com/object?${query}` }); }],
  ];

  it.each(failures)("falls back to the archive location with a clean file when %s", (_name, arrange) => withFixture(async (f) => {
    arrange(f);
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
    await expect(f.restored()).resolves.toBe("from archive location");
    expect(f.archiveReads()).toHaveLength(1);
    expect(f.archiveReads()[0]?.archiveDirectoryFiles).toEqual([]);
    expect(f.storageReads().length).toBeLessThanOrEqual(1);
    expect(f.seen.some((request) => request.path === "/storage/followed")).toBe(false);
    expect(f.completions()).toHaveLength(0);
  }));

  it("never opens a descriptor above the archive limit", () => withFixture(async (f) => {
    f.scenario.transfer = (base) => ({ ...base, size: 10 * 1024 ** 3 + 1 });
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
    expect(f.storageReads()).toHaveLength(0);
    expect(f.archiveReads()).toHaveLength(1);
  }));

  it("falls back when direct bytes stall past the idle timeout", () => withFixture(async (f) => {
    f.scenario.storage = (response) => {
      response.writeHead(200, { "Content-Length": f.directBytes.length });
      response.write(f.directBytes.subarray(0, 8));
    };
    await expect(f.client.restoreCache(["node_modules"], primaryKey, [], { segmentTimeoutInMs: 50 }))
      .resolves.toBe(primaryKey);
    await expect(f.restored()).resolves.toBe("from archive location");
    expect(f.archiveReads()[0]?.archiveDirectoryFiles).toEqual([]);
    expect(f.completions()).toHaveLength(0);
  }));

  it("verifies size and digest before validating or touching the workspace", () => withFixture(async (f) => {
    const hostile = await gzipArchive("../layercache-escape.txt", "must not be read");
    await mkdir(join(f.workspace, "node_modules/widget"), { recursive: true });
    await writeFile(join(f.workspace, "node_modules/widget/index.js"), "sentinel");
    f.scenario.storage = (response) => { response.end(hostile); };
    f.scenario.transfer = (base) => ({ ...base, digest: `sha256:${"0".repeat(64)}`, size: hostile.length });
    f.scenario.archive = (response) => { response.statusCode = 500; response.end(); };
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).rejects.toThrow("HTTP 500");
    await expect(f.restored()).resolves.toBe("sentinel");
    await expect(readdir(join(f.workspace, ".."))).resolves.not.toContain("layercache-escape.txt");
  }));

  it("ignores a descriptor on a public-origin response", () => withFixture(async (f) => {
    f.scenario.publicMetadata = (version, digest, size) => {
      const nativeKey = actionsPublicNativeKey(identity, primaryKey, version);
      const exactInputs = actionsPublicInputs(identity, primaryKey, version);
      const publication = {
        integration: "actions",
        project: identity.repository,
        compatibility: identity.compatibility,
        nativeKey,
        repository: `https://github.com/${identity.repository}`,
        commit: identity.commit,
        recipeDigest: identity.recipeDigest,
        target: identity.target,
        platform: identity.platform,
        inputs: exactInputs,
        toolchain: identity.toolchain,
        builder: identity.builder,
        builderImageDigest: `sha256:${"c".repeat(64)}`,
        digest,
        size,
        durationMs: 1,
        buildId: "build-123",
        issuedAt: new Date(Date.now() - 60_000).toISOString(),
        expiresAt: new Date(Date.now() + 60 * 60_000).toISOString(),
      };
      const publicIdentity = publicationIdentity(publication);
      const payload = Buffer.from(JSON.stringify({
        _type: "https://in-toto.io/Statement/v1",
        subject: [{ name: `layercache-public:${publicIdentity}`, digest: { sha256: digest } }],
        predicateType: "https://layercache.dev/attestation/public-cache/v1",
        predicate: publication,
      }));
      return {
        request: {
          cacheIdentity: createHash("sha256").update([
            "actions", identity.repository, identity.compatibility, nativeKey,
          ].join("\0")).digest("hex"),
          expected: {
            Integration: "actions",
            Project: identity.repository,
            Compatibility: identity.compatibility,
            NativeKey: nativeKey,
            Target: identity.target,
            Inputs: exactInputs,
          },
          repository: identity.repository,
          sourceRepository: `https://github.com/${identity.repository}`,
          ref: identity.ref,
          key: primaryKey,
          version,
          compatibility: identity.compatibility,
          sourceCommit: identity.commit,
          recipeDigest: identity.recipeDigest,
          target: identity.target,
          platform: identity.platform,
          toolchain: identity.toolchain,
          builder: identity.builder,
        },
        envelope: {
          payloadType,
          payload: payload.toString("base64"),
          signatures: [{ keyid: "test", sig: sign(null, pae(payloadType, payload), f.privateKey).toString("base64") }],
        },
        digest,
        size,
        expiresAt: publication.expiresAt,
        publicIdentity,
      };
    };
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
    await expect(f.restored()).resolves.toBe("from archive location");
    expect(f.storageReads()).toHaveLength(0);
    expect(f.archiveReads()).toHaveLength(1);
    expect(f.completions()).toHaveLength(0);
  }));

  it.each<[string, (response: ServerResponse) => void]>([
    ["rejected", (response) => { response.statusCode = 404; response.end(); }],
    ["dropped", (response) => { response.destroy(); }],
  ])("tolerates a %s completion", (_name, complete) => withFixture(async (f) => {
    f.scenario.complete = complete;
    await expect(f.client.restoreCache(["node_modules"], primaryKey)).resolves.toBe(primaryKey);
    await expect(f.restored()).resolves.toBe("from storage");
    expect(f.completions()).toHaveLength(1);
    expect(f.archiveReads()).toHaveLength(0);
  }));

  it("never surfaces the presigned query when both paths fail", () => withFixture(async (f) => {
    f.scenario.storage = (response) => { response.destroy(); };
    f.scenario.archive = (response) => { response.statusCode = 500; response.end(); };
    const error: unknown = await f.client.restoreCache(["node_modules"], primaryKey).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(Error);
    expect(inspect(error, { depth: null, showHidden: true })).not.toContain(secret);
  }));
});
