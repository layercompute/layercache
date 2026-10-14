import { setTimeout as delay } from 'node:timers/promises';

const children = new WeakMap();

export function trackChild(child) {
  const state = { closed: false };
  state.closedPromise = new Promise(resolve => child.once('close', () => {
    state.closed = true;
    resolve();
  }));
  children.set(child, state);
  return child;
}

export function signalChild(child, signal) {
  try {
    if (process.platform !== 'win32') process.kill(-child.pid, signal);
    else child.kill(signal);
  } catch (error) {
    if (error.code !== 'ESRCH') throw error;
  }
}

async function waitForClose(state, timeout) {
  const timer = new AbortController();
  try {
    return await Promise.race([
      state.closedPromise.then(() => true),
      delay(timeout, false, { signal: timer.signal }),
    ]);
  } finally {
    timer.abort();
  }
}

export async function stopChild(child, { graceMs = 5000, killMs = 2000 } = {}) {
  if (!child) return;
  if (!children.has(child)) trackChild(child);
  const state = children.get(child);
  if (state.closed) return;
  state.stopping ??= (async () => {
    if (child.pid) signalChild(child, 'SIGTERM');
    if (await waitForClose(state, graceMs)) return;
    if (child.pid) signalChild(child, 'SIGKILL');
    if (!await waitForClose(state, killMs)) throw new Error(`QA child ${child.pid} did not close after SIGKILL`);
    throw new Error(`QA child ${child.pid} required forced shutdown`);
  })();
  return state.stopping;
}

// Every owner receives a stop attempt before any failure is reported. Parallel
// shutdown bounds the whole cleanup by one grace interval and one kill interval.
export async function stopChildren(owned, options) {
  const results = await Promise.allSettled([...owned].reverse().map(child => stopChild(child, options)));
  const errors = results.filter(result => result.status === 'rejected').map(result => result.reason);
  if (errors.length) throw new AggregateError(errors, 'Nx QA child cleanup failed');
}

export function cleanupOnSignals(owned, cancellation, options) {
  let cleanup;
  const handlers = new Map(['SIGINT', 'SIGTERM'].map((signal, index) => [signal, () => {
    cleanup ??= stopChildren(owned, options);
    void cleanup.catch(() => {});
    process.exitCode = index === 0 ? 130 : 143;
    cancellation.abort(new Error(`Nx QA interrupted by ${signal}`));
  }]));
  for (const [signal, handler] of handlers) process.once(signal, handler);
  return async () => {
    for (const [signal, handler] of handlers) process.removeListener(signal, handler);
    if (cleanup) await cleanup;
  };
}
