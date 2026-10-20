import ncc from "@vercel/ncc";
import { copyFile, mkdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const temporary = resolve(root, "dist/worker");
try {
  await mkdir(temporary, { recursive: true });
  const entry = resolve(temporary, "entry.ts");
  // Keep ncc on this action's TypeScript configuration and dependencies.
  await writeFile(entry, 'import "../../../setup/execution-worker.ts";\n');
  const { code, assets } = await ncc(entry, {
    esm: false,
    filename: "index.js",
    minify: true,
  });
  if (Object.keys(assets).length !== 0) {
    throw new Error("Execution observation worker has unexpected assets");
  }
  const compiled = resolve(temporary, "index.js");
  await writeFile(compiled, code);
  for (const entry of ["main", "post"]) {
    await copyFile(compiled, resolve(root, "dist", entry, "execution-worker.cjs"));
  }
} finally {
  await rm(temporary, { recursive: true, force: true });
}
