export const cloudflareTestPrefix = process.env.CI ? "dns-api-ci-" : "dns-api-local-";

export async function generateCloudflareTestDomainName(
  seed: string,
  zoneExists: (name: string) => Promise<boolean>,
): Promise<string> {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    const candidate = cloudflareTestDomainName(attempt === 0 ? seed : `${seed}-${randomBase36()}`);
    if (!(await zoneExists(candidate))) {
      return candidate;
    }
  }
  throw new Error("Could not generate an unused Cloudflare test zone name");
}

export function cloudflareTestDomainName(seed: string, now = new Date()): string {
  const yyyymmdd = [
    now.getUTCFullYear(),
    String(now.getUTCMonth() + 1).padStart(2, "0"),
    String(now.getUTCDate()).padStart(2, "0"),
  ].join("");
  const hhmm = [
    String(now.getUTCHours()).padStart(2, "0"),
    String(now.getUTCMinutes()).padStart(2, "0"),
  ].join("");
  return `${cloudflareTestPrefix}${yyyymmdd}-${hhmm}-${shortID(seed)}.com`;
}

function shortID(seed: string): string {
  const normalized = seed.toLowerCase().replace(/[^a-z0-9]+/g, "").slice(0, 24);
  if (normalized.length >= 6) {
    return normalized;
  }
  return `${normalized}${randomBase36()}`.slice(0, 12);
}

function randomBase36(): string {
  return Math.random().toString(36).slice(2, 10);
}
