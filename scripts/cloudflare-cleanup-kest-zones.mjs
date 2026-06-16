const accountID = process.env.CF_ACCOUNT_ID;
const token = process.env.CF_API_TOKEN;
const requestedPrefix = process.env.PREFIX ?? "";
const confirmPrefix = process.env.CONFIRM_PREFIX ?? "";
const shouldDelete = process.env.DELETE === "true" || process.env.DELETE === "1";
const defaultPrefix = process.env.CLOUDFLARE_ZONE_NAME_PREFIX ?? (process.env.CI ? "dns-api-ci-" : "dns-api-local-");
const broadPrefixes = new Set(["", "dns-api-", "dns-api", "*", ".com"]);

if (!accountID) {
  throw new Error("CF_ACCOUNT_ID is required");
}
if (!token) {
  throw new Error("CF_API_TOKEN is required");
}

const effectivePrefix = requestedPrefix || defaultPrefix;
if (broadPrefixes.has(effectivePrefix)) {
  throw new Error("PREFIX is too broad for Cloudflare kest cleanup");
}
if (!effectivePrefix.startsWith(defaultPrefix)) {
  throw new Error(`PREFIX must start with ${defaultPrefix}`);
}
if (shouldDelete && confirmPrefix !== effectivePrefix) {
  throw new Error("CONFIRM_PREFIX must exactly match PREFIX when DELETE=true");
}

const zones = await listZones();
let matches = zones.filter((zone) => zone.name.startsWith(effectivePrefix));

if (matches.length === 0) {
  console.log("no Cloudflare kest zones found");
  process.exit(0);
}

for (const zone of matches) {
  console.log(`${zone.id} ${zone.name} status=${zone.status} created=${zone.created_on ?? "-"}`);
  if (shouldDelete) {
    await cloudflareFetch(`/zones/${encodeURIComponent(zone.id)}`, { method: "DELETE" });
    console.log(`deleted ${zone.id}`);
  }
}

if (shouldDelete) {
  matches = (await listZones()).filter((zone) => zone.name.startsWith(effectivePrefix));
  if (matches.length === 0) {
    console.log("no Cloudflare kest zones remain");
  } else {
    console.log("remaining Cloudflare kest zones:");
    for (const zone of matches) {
      console.log(`${zone.id} ${zone.name} status=${zone.status} created=${zone.created_on ?? "-"}`);
    }
  }
}

async function listZones() {
  const result = [];
  let page = 1;
  let totalPages = 1;
  do {
    const params = new URLSearchParams({
      "account.id": accountID,
      page: String(page),
      per_page: "50",
    });
    const body = await cloudflareFetch(`/zones?${params}`);
    result.push(...body.result);
    totalPages = body.result_info?.total_pages ?? page;
    page += 1;
  } while (page <= totalPages);
  return result;
}

async function cloudflareFetch(path, init = {}) {
  const response = await fetch(`https://api.cloudflare.com/client/v4${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
      ...(init.headers ?? {}),
    },
  });
  const body = response.status === 204 ? { success: true, result: null } : await response.json();
  if (!response.ok || body.success === false) {
    const message = body.errors?.[0]?.message ?? `Cloudflare API returned HTTP ${response.status}`;
    throw new Error(message);
  }
  return body;
}
