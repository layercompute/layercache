// QA only. Observe the origin without handling the exception or inspecting any
// error fields. Node retains its normal fatal behavior and private diagnostics.
process.on('uncaughtExceptionMonitor', (_error, origin) => {
  try {
    if (origin === 'uncaughtException') process.stderr.write('Archive subprocess fatal origin: uncaughtException\n');
    else if (origin === 'unhandledRejection') process.stderr.write('Archive subprocess fatal origin: unhandledRejection\n');
  } catch { /* Diagnostic output must not replace the original fatal error. */ }
});
