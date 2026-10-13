import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";

const task = process.argv[2];
const fixture = JSON.parse(readFileSync(new URL("fixture.json", import.meta.url)));
const dependency = JSON.parse(readFileSync(new URL("../../dependency.json", import.meta.url)));
const result = {
  task,
  source: fixture.source,
  dependency: dependency.version,
  profile: process.env.TEST_PROFILE,
  passed: true,
};

appendFileSync(process.env.EXECUTION_COUNTER, JSON.stringify({ ...result, fail: fixture.fail }) + "\n");
if (task === "test" && fixture.fail) {
  console.error("intentional test failure");
  process.exit(7);
}

const directory = task === "test" ? ".test" : "dist";
mkdirSync(new URL(`${directory}/`, import.meta.url), { recursive: true });
writeFileSync(new URL(`${directory}/result.json`, import.meta.url), JSON.stringify(result) + "\n");
console.log("executed", task, fixture.source);
