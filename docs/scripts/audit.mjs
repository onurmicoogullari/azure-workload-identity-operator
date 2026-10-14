// Fails when npm audit reports a high or critical advisory that is not
// explicitly allowlisted below, or when an allowlist entry has expired. Only
// allowlist advisories that have no patched release, and remove entries as soon
// as a fix ships. To renew an expired entry, re-check that no fix exists and
// that the reason still holds, then move the expiry at most 90 days ahead.
import { spawnSync } from "node:child_process";

const allowlist = new Map([
  [
    "GHSA-vfj7-8cjw-p6xm",
    {
      name: "braces",
      reason:
        "No patched release exists. Only reached at build time through chokidar, " +
        "micromatch, and markdownlint-cli2 on glob patterns from this repository.",
      owner: "@onurmicoogullari",
      expires: "2027-01-12",
    },
  ],
]);

const failingSeverities = new Set(["high", "critical"]);

const result = spawnSync("npm", ["audit", "--include=dev", "--json"], {
  encoding: "utf8",
  maxBuffer: 64 * 1024 * 1024,
});
if (!result.stdout) {
  console.error(result.stderr);
  process.exit(1);
}

const report = JSON.parse(result.stdout);
const advisories = new Map();
for (const vulnerability of Object.values(report.vulnerabilities ?? {})) {
  for (const via of vulnerability.via) {
    if (typeof via === "object") {
      advisories.set(via.url.split("/").pop(), via);
    }
  }
}

const today = new Date().toISOString().slice(0, 10);
const failures = [];
for (const [id, advisory] of advisories) {
  if (!failingSeverities.has(advisory.severity)) continue;
  const entry = allowlist.get(id);
  if (entry && entry.expires >= today) {
    console.log(
      `Allowlisted ${advisory.severity} advisory ${id} (${advisory.name}) until ${entry.expires}, owner ${entry.owner}: ${advisory.title}`,
    );
    continue;
  }
  if (entry) {
    failures.push(`${advisory.severity} ${id} (${advisory.name}): allowlist entry expired on ${entry.expires}, owner ${entry.owner} must review it`);
    continue;
  }
  failures.push(`${advisory.severity} ${id} (${advisory.name} ${advisory.range}): ${advisory.title}`);
}

for (const [id, entry] of allowlist) {
  if (!advisories.has(id)) {
    console.log(`Allowlist entry ${id} (${entry.name}) no longer matches any advisory and can be removed`);
  }
}

if (failures.length > 0) {
  console.error(`Found ${failures.length} high or critical advisories that need action:`);
  for (const failure of failures) console.error(`  ${failure}`);
  process.exit(1);
}
console.log("No unallowlisted high or critical advisories found");
