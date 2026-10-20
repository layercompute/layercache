import { describe, expect, it, vi } from "vitest";

import * as execution from "../../setup/execution.js";
import {
  compatibilityForPlatform,
  platformForRuntime,
  runRestore,
  runSave,
  type ActionCore,
  type CacheClient,
} from "../src/action.js";

describe("restore action", () => {
  it("exchanges GitHub OIDC for a short-lived project token", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/layercache",
      project: "github.com/acme/widget",
      path: "node_modules",
      key: "oidc-key",
      "public-cache-mode": "disabled",
      compatibility: "linux-amd64-schema1-node@24",
    }, "github-oidc-token");
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn(),
    };
    const exchange = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      teamToken: "short-lived-team-token",
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", exchange);
    try {
      const env: NodeJS.ProcessEnv = { GITHUB_JOB: "public-cache" };
      await runRestore({ core, cache, env });
      expect(core.oidcAudiences).toEqual(["layercache:github.com/acme/widget"]);
      expect(exchange).toHaveBeenCalledWith(
        new URL("https://cache.layercache.example/layercache/v1/auth/github-oidc/exchange"),
		expect.objectContaining({ method: "POST", redirect: "error" }),
      );
      const exchangeBody = JSON.parse(String((exchange.mock.calls[0]?.[1] as RequestInit).body));
      expect(exchangeBody).toMatchObject({
        project: "github.com/acme/widget",
        target: "",
      });
      expect(env.ACTIONS_RUNTIME_TOKEN).toBe("short-lived-team-token");
      expect(core.secrets).toContain("short-lived-team-token");
      expect(exchange).toHaveBeenCalledTimes(1);
      expect(core.failures).toEqual([]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("observes only fresh masked OIDC tokens and uses start and finish phases", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/layercache",
      project: "github.com/acme/widget",
      path: "node_modules",
      key: "observed-key",
      compatibility: "linux-amd64-schema1-node@24",
      "execution-provider": "ubicloud",
    }, "github-oidc-token");
    const env = { GITHUB_JOB: "quality", RUNNER_NAME: "private-runner-name" };
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn().mockResolvedValue(42),
    };
    const observations = vi.spyOn(execution, "reportExecutionObservation").mockImplementation(async (options) => {
      expect(core.secrets).toContain((options.exchange as { teamToken: string }).teamToken);
      if (options.phase === "start") {
        expect(cache.restoreCache).not.toHaveBeenCalled();
      } else {
        expect(cache.saveCache).not.toHaveBeenCalled();
      }
    });
    let tokenNumber = 0;
    const exchange = vi.fn().mockImplementation(async () => new Response(JSON.stringify({
      teamToken: `short-lived-team-token-${++tokenNumber}`,
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      executionObservations: { version: 1, path: "/v1/executions/observations" },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", exchange);
    try {
      await runRestore({ core, cache, env });
      await runSave({ core, cache, env });

      expect(observations).toHaveBeenCalledTimes(2);
      expect(observations.mock.calls.map(([options]) => ({
        endpoint: options.endpoint,
        project: options.project,
        phase: options.phase,
        provider: options.provider,
        env: options.env,
        token: (options.exchange as { teamToken: string }).teamToken,
      }))).toEqual([
        {
          endpoint: "https://cache.layercache.example/layercache",
          project: "github.com/acme/widget",
          phase: "start",
          provider: "ubicloud",
          env,
          token: "short-lived-team-token-1",
        },
        {
          endpoint: "https://cache.layercache.example/layercache",
          project: "github.com/acme/widget",
          phase: "finish",
          provider: "ubicloud",
          env,
          token: "short-lived-team-token-2",
        },
      ]);
      expect(exchange).toHaveBeenCalledTimes(2);
      for (const [, request] of exchange.mock.calls) {
        expect(JSON.parse(String((request as RequestInit).body))).toEqual({
          project: "github.com/acme/widget",
          compatibility: "linux-amd64-schema1-node@24",
          idToken: "github-oidc-token",
          recipeDigest: "",
          target: "",
          platform: platformForRuntime(process.platform, process.arch),
          toolchain: "actions/cache@6.2.0",
          builder: "",
        });
      }
      expect(cache.restoreCache).toHaveBeenCalledTimes(1);
      expect(cache.saveCache).toHaveBeenCalledWith(["node_modules"], "observed-key");
      expect(core.failures).toEqual([]);
      expect(core.warnings).toEqual([]);
    } finally {
      observations.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it.each([false, true])("does not observe static-token authentication after OIDC fallback=%s", async (useOIDCFallback) => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      project: useOIDCFallback ? "github.com/acme/widget" : "",
      token: "static-team-token",
      path: "node_modules",
      key: "static-key",
      "execution-provider": "ubicloud",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn().mockResolvedValue(42),
    };
    const observations = vi.spyOn(execution, "reportExecutionObservation");
    const exchange = vi.fn();
    vi.stubGlobal("fetch", exchange);
    try {
      await runRestore({ core, cache, env: {} });
      await runSave({ core, cache, env: {} });

      expect(observations).not.toHaveBeenCalled();
      expect(exchange).not.toHaveBeenCalled();
      expect(cache.restoreCache).toHaveBeenCalledTimes(1);
      expect(cache.saveCache).toHaveBeenCalledTimes(1);
      expect(core.failures).toEqual([]);
    } finally {
      observations.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it("does not exchange or observe again when an exact restore skips the post save", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      project: "github.com/acme/widget",
      path: "node_modules",
      key: "exact-observed-key",
    }, "github-oidc-token");
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue("exact-observed-key"),
      saveCache: vi.fn(),
    };
    const observations = vi.spyOn(execution, "reportExecutionObservation").mockResolvedValue(undefined);
    const exchange = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      teamToken: "short-lived-team-token",
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      executionObservations: { version: 1, path: "/v1/executions/observations" },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", exchange);
    try {
      await runRestore({ core, cache, env: {} });
      await runSave({ core, cache, env: {} });

      expect(observations).toHaveBeenCalledTimes(1);
      expect(observations).toHaveBeenCalledWith(expect.objectContaining({ phase: "start", provider: "" }));
      expect(exchange).toHaveBeenCalledTimes(1);
      expect(cache.saveCache).not.toHaveBeenCalled();
      expect(core.failures).toEqual([]);
    } finally {
      observations.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it.each(["http", "network"])("keeps fresh cache credentials when optional observations fail with %s errors", async (failure) => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/layercache",
      project: "github.com/acme/widget",
      token: "static-fallback-token",
      path: "node_modules",
      key: "optional-report-failure",
      "execution-provider": "private-runner-name",
    }, "github-oidc-token");
    const env = {
      GITHUB_WORKSPACE: "/private/workspace",
      RUNNER_NAME: "private-runner-name",
      PRIVATE_TEST_SECRET: "private-test-secret",
    };
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn().mockResolvedValue(42),
    };
    const sample: execution.ExecutionSample = {
      os: "linux",
      arch: "amd64",
      cpu: {
        visibleLogical: 8,
        affinityLogical: 2,
        quotaMilliCPU: 2000,
        effectiveMilliCPU: 2000,
        limitKnowledge: "complete",
      },
      memory: {
        visibleTotalBytes: 8 * 1024 ** 3,
        cgroupLimitBytes: 4 * 1024 ** 3,
        effectiveLimitBytes: 4 * 1024 ** 3,
        limitKnowledge: "complete",
      },
      filesystems: [],
    };
    const collect = vi.fn().mockResolvedValue(sample);
    const report = execution.reportExecutionObservation;
    const observation = vi.spyOn(execution, "reportExecutionObservation")
      .mockImplementation((options) => report({ ...options, collect }));
    const bodies: string[] = [];
    const fetcher = vi.fn().mockImplementation(async (input: URL, request: RequestInit) => {
      if (input.pathname.endsWith("/auth/github-oidc/exchange")) {
        return new Response(JSON.stringify({
          teamToken: "fresh-team-token",
          expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
          executionObservations: { version: 1, path: "/v1/executions/observations" },
        }), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      expect(input.href).toBe("https://cache.layercache.example/v1/executions/observations");
      expect(request.redirect).toBe("error");
      expect(request.headers).toEqual({
        Authorization: "Bearer fresh-team-token",
        "Content-Type": "application/json",
        "X-LayerCache-Project": "github.com/acme/widget",
      });
      bodies.push(String(request.body));
      if (failure === "network") throw new TypeError("private service detail");
      return new Response("private service detail", { status: 503 });
    });
    vi.stubGlobal("fetch", fetcher);
    try {
      await runRestore({ core, cache, env });
      await runSave({ core, cache, env });

      expect(collect).toHaveBeenCalledTimes(2);
      expect(bodies.map((body) => JSON.parse(body))).toEqual([
        { version: 1, phase: "start", provider: "unknown", sample },
        { version: 1, phase: "finish", provider: "unknown", sample },
      ]);
      for (const body of bodies) {
        expect(Buffer.byteLength(body)).toBeLessThanOrEqual(16 * 1024);
        expect(body).not.toMatch(/private|fresh-team-token|static-fallback-token|github-oidc-token/);
      }
      expect(fetcher).toHaveBeenCalledTimes(4);
      expect(env).toMatchObject({ ACTIONS_RUNTIME_TOKEN: "fresh-team-token" });
      expect(cache.restoreCache).toHaveBeenCalledTimes(1);
      expect(cache.saveCache).toHaveBeenCalledTimes(1);
      expect(core.failures).toEqual([]);
      expect(core.warnings).toEqual([]);
    } finally {
      observation.mockRestore();
      vi.unstubAllGlobals();
    }
  });

  it("continues as a cache miss when GitHub OIDC is unavailable and no fallback token is configured", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      project: "github.com/acme/widget",
      path: "node_modules",
      key: "oidc-unavailable-key",
      "fail-on-cache-miss": "true",
      "public-cache-mode": "disabled",
      compatibility: "linux-amd64-schema1-node@24",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn(),
      saveCache: vi.fn(),
    };
    const env: NodeJS.ProcessEnv = {};

    await runRestore({ core, cache, env });

    expect(cache.restoreCache).not.toHaveBeenCalled();
    expect(core.outputs).toEqual({
      "cache-primary-key": "oidc-unavailable-key",
      "cache-matched-key": "",
      "cache-hit": "",
    });
    expect(core.warnings).toEqual([
      expect.stringContaining("authentication is unavailable; continuing as a cache miss"),
    ]);
    expect(core.failures).toEqual([
      "Layer Cache authentication was unavailable and fail-on-cache-miss is enabled.",
    ]);
    expect(env.ACTIONS_CACHE_URL).toBeUndefined();
    expect(env.ACTIONS_RUNTIME_TOKEN).toBeUndefined();
  });

  it.each([
    {
      name: "returns a non-success status",
      failure: () => Promise.resolve(new Response("unavailable", { status: 503 })),
      message: "OIDC exchange returned HTTP 503",
    },
    {
      name: "times out",
      failure: () => Promise.reject(new DOMException("request timed out", "TimeoutError")),
      message: "request timed out",
    },
  ])("continues as a cache miss when the OIDC exchange $name", async ({ failure, message }) => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      project: "github.com/acme/widget",
      path: "node_modules",
      key: "oidc-exchange-failure-key",
      "public-cache-mode": "disabled",
      compatibility: "linux-amd64-schema1-node@24",
    }, "github-oidc-token");
    const cache: CacheClient = {
      restoreCache: vi.fn(),
      saveCache: vi.fn(),
    };
    vi.stubGlobal("fetch", vi.fn().mockImplementation(failure));
    try {
      await runRestore({ core, cache, env: {} });

      expect(cache.restoreCache).not.toHaveBeenCalled();
      expect(core.warnings).toEqual([expect.stringContaining(message)]);
      expect(core.failures).toEqual([]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("uses the Layer Cache v1 endpoint and publishes stock exact-hit outputs", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1",
      token: "team-token",
      path: "node_modules\n.turbo",
      key: "linux-node-abc",
      "restore-keys": "linux-node-\nlinux-",
      "lookup-only": "false",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "disabled",
      compatibility: "linux-amd64-schema1-node@24",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue("linux-node-abc"),
      saveCache: vi.fn(),
    };
    const env: NodeJS.ProcessEnv = {
      ACTIONS_CACHE_SERVICE_V2: "true",
      ACTIONS_RESULTS_URL: "https://results.github.example/",
      ACTIONS_CACHE_URL: "https://cache.github.example/",
      ACTIONS_RUNTIME_TOKEN: "github-token",
      GITHUB_SERVER_URL: "https://github.com",
    };

    await runRestore({ core, cache, env });

    expect(cache.restoreCache).toHaveBeenCalledWith(
      ["node_modules", ".turbo"],
      "linux-node-abc",
      ["linux-node-", "linux-"],
      { lookupOnly: false },
    );
    expect(core.outputs).toEqual({
      "cache-primary-key": "linux-node-abc",
      "cache-matched-key": "linux-node-abc",
      "cache-hit": "true",
    });
    expect(core.states).toMatchObject({
      "cache-primary-key": "linux-node-abc",
      "cache-matched-key": "linux-node-abc",
      "cache-paths": '["node_modules",".turbo"]',
    });
    expect(core.secrets).toEqual(["team-token"]);
    expect(core.failures).toEqual([]);
    expect(env.ACTIONS_CACHE_URL).toBe(
      "https://cache.layercache.example/v1/_layercache/compatibility/linux-amd64-schema1-node%4024/",
    );
    expect(env.ACTIONS_RUNTIME_TOKEN).toBe("team-token");
    expect(env.ACTIONS_CACHE_SERVICE_V2).toBeUndefined();
    expect(env.ACTIONS_RESULTS_URL).toBe("https://results.github.example/");
    expect(env.GITHUB_SERVER_URL).toBe("https://github.com");
  });

  it("treats a remote authorization failure as a warning and cache miss", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      token: "expired-project-token",
      path: "node_modules",
      key: "remote-auth-failure",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "disabled",
      compatibility: "linux-amd64-schema1-node@24",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockRejectedValue(new Error("Layer Cache lookup returned HTTP 401")),
      saveCache: vi.fn(),
    };

    await runRestore({ core, cache, env: {} });

    expect(core.outputs).toEqual({
      "cache-primary-key": "remote-auth-failure",
      "cache-matched-key": "",
      "cache-hit": "",
    });
    expect(core.warnings).toEqual([
      expect.stringContaining("will continue as a cache miss: Layer Cache lookup returned HTTP 401"),
    ]);
    expect(core.failures).toEqual([]);
  });

  it("derives the configured namespace from the Node job platform", () => {
    expect(compatibilityForPlatform("linux", "x64")).toBe("linux-amd64-schema1");
    expect(compatibilityForPlatform("darwin", "arm64")).toBe("darwin-arm64-schema1");
    expect(() => compatibilityForPlatform("win32", "x64")).toThrow("unsupported Node job platform");
  });

  it("rejects a non-canonical compatibility override before contacting the cache", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/",
      project: "github.com/acme/widget",
      compatibility: "Linux X64",
      path: "node_modules",
      key: "key",
      "public-cache-mode": "disabled",
    });
    const cache: CacheClient = { restoreCache: vi.fn(), saveCache: vi.fn() };
    const exchange = vi.fn();
    vi.stubGlobal("fetch", exchange);

    try {
      await runRestore({ core, cache, env: {} });

      expect(exchange).not.toHaveBeenCalled();
      expect(cache.restoreCache).not.toHaveBeenCalled();
      expect(core.failures).toEqual([
        "Layer Cache compatibility must be at most 256 bytes and use lowercase ASCII letters, digits, and -_.:+@ delimiters.",
      ]);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("reports a restore-key hit and saves the primary key in the post action", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1/",
      token: "team-token",
      path: "node_modules",
      key: "linux-node-new",
      "restore-keys": "linux-node-",
      "lookup-only": "false",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "disabled",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue("linux-node-old"),
      saveCache: vi.fn().mockResolvedValue(42),
    };
    const env: NodeJS.ProcessEnv = {};

    await runRestore({ core, cache, env });
    await runSave({ core, cache, env });

    expect(core.outputs).toMatchObject({
      "cache-primary-key": "linux-node-new",
      "cache-matched-key": "linux-node-old",
      "cache-hit": "false",
    });
    expect(cache.saveCache).toHaveBeenCalledWith(["node_modules"], "linux-node-new");
    expect(core.failures).toEqual([]);
  });

  it("publishes empty miss outputs, fails when requested, and skips lookup-only save", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1/",
      token: "team-token",
      path: "node_modules",
      key: "missing-key",
      "restore-keys": "",
      "lookup-only": "true",
      "fail-on-cache-miss": "true",
      "public-cache-mode": "disabled",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn(),
    };
    const env: NodeJS.ProcessEnv = {};

    await runRestore({ core, cache, env });
    await runSave({ core, cache, env });

    expect(cache.restoreCache).toHaveBeenCalledWith(["node_modules"], "missing-key", [], { lookupOnly: true });
    expect(cache.saveCache).not.toHaveBeenCalled();
    expect(core.outputs).toEqual({
      "cache-primary-key": "missing-key",
      "cache-matched-key": "",
      "cache-hit": "",
    });
    expect(core.failures).toEqual([
      "Layer Cache did not find a matching cache entry and fail-on-cache-miss is enabled.",
    ]);
  });

  it("rejects unknown Public Cache restore modes", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1/",
      token: "team-token",
      path: "node_modules",
      key: "public-key",
      "restore-keys": "",
      "lookup-only": "false",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "verified-artifact",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn(),
      saveCache: vi.fn(),
    };

    await runRestore({ core, cache, env: {} });

    expect(cache.restoreCache).not.toHaveBeenCalled();
    expect(core.failures).toEqual([
      "Layer Cache public-cache-mode must be disabled or verified.",
    ]);
  });

  it("suppresses post save after an exact primary-key hit", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1/",
      token: "team-token",
      path: "node_modules",
      key: "exact-key",
      "restore-keys": "",
      "lookup-only": "false",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "disabled",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue("exact-key"),
      saveCache: vi.fn(),
    };
    const env: NodeJS.ProcessEnv = {};

    await runRestore({ core, cache, env });
    await runSave({ core, cache, env });

    expect(cache.saveCache).not.toHaveBeenCalled();
    expect(core.failures).toEqual([]);
  });

  it("does not report success when the cache client declines publication", async () => {
    const core = new FakeCore({
      endpoint: "https://cache.layercache.example/v1/",
      token: "team-token",
      path: "node_modules",
      key: "missed-key",
      "restore-keys": "",
      "lookup-only": "false",
      "fail-on-cache-miss": "false",
      "public-cache-mode": "disabled",
    });
    const cache: CacheClient = {
      restoreCache: vi.fn().mockResolvedValue(undefined),
      saveCache: vi.fn().mockResolvedValue(-1),
    };

    await runRestore({ core, cache, env: {} });
    await runSave({ core, cache, env: {} });

    expect(cache.saveCache).toHaveBeenCalledWith(["node_modules"], "missed-key");
    expect(core.infos).not.toContain("Layer Cache saved missed-key.");
    expect(core.warnings).toContain("Layer Cache did not publish missed-key; see the cache client diagnostics.");
    expect(core.failures).toEqual([]);
  });
});

class FakeCore implements ActionCore {
  readonly outputs: Record<string, string> = {};
  readonly states: Record<string, string> = {};
  readonly failures: string[] = [];
  readonly secrets: string[] = [];
  readonly infos: string[] = [];
  readonly warnings: string[] = [];
  readonly oidcAudiences: string[] = [];

  constructor(
    private readonly inputs: Record<string, string>,
    private readonly oidcToken = "",
  ) {}

  getInput(name: string, options?: { required?: boolean }): string {
    const value = this.inputs[name] ?? "";
    if (options?.required && value === "") {
      throw new Error(`Input required and not supplied: ${name}`);
    }
    return value;
  }

  getMultilineInput(name: string, options?: { required?: boolean }): string[] {
    const value = this.getInput(name, options);
    return value === ""
      ? []
      : value
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean);
  }

  getBooleanInput(name: string): boolean {
    return this.getInput(name).toLowerCase() === "true";
  }

  async getIDToken(audience = ""): Promise<string> {
    this.oidcAudiences.push(audience);
    if (this.oidcToken === "") {
      throw new Error("GitHub OIDC is unavailable in this test");
    }
    return this.oidcToken;
  }

  setOutput(name: string, value: unknown): void {
    this.outputs[name] = String(value);
  }

  saveState(name: string, value: unknown): void {
    this.states[name] = String(value);
  }

  getState(name: string): string {
    return this.states[name] ?? "";
  }

  setFailed(message: string | Error): void {
    this.failures.push(String(message instanceof Error ? message.message : message));
  }

  setSecret(secret: string): void {
    this.secrets.push(secret);
  }

  info(message: string): void {
    this.infos.push(message);
  }

  warning(message: string | Error): void {
    this.warnings.push(String(message instanceof Error ? message.message : message));
  }
}
