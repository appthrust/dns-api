import { type K8sResource, type Namespace, type Scenario } from "@appthrust/kest";
import { expect } from "bun:test";
import { generateCloudflareTestDomainName } from "./cloudflare-test-names";

export const cloudflareProvider = { name: "cloudflare.dns.appthrust.io", version: "v1alpha1" };
export const waitForCloudflare = {
  timeout: "5m",
  interval: "5s",
  stallTimeout: "0s",
};

const cloudflareAccountID = requireEnv("CF_ACCOUNT_ID");
const cloudflareAPIToken = requireEnv("CF_API_TOKEN");

type RecordType = "A" | "AAAA" | "TXT" | "CNAME" | "MX" | "CAA" | "NS";

interface Condition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
}

interface CloudflareIdentity extends K8sResource {
  apiVersion: "cloudflare.dns.appthrust.io/v1alpha1";
  kind: "CloudflareIdentity";
  spec: {
    accessToken: {
      secretRef: {
        name: string;
        key: string;
      };
    };
  };
  status?: {
    account?: {
      id?: string;
    };
    conditions?: Array<Condition>;
  };
}

interface ZoneClass extends K8sResource {
  apiVersion: "dns.appthrust.io/v1alpha1";
  kind: "ZoneClass";
  spec: {
    allowedZones: {
      namespaces: {
        from: "Selector";
        selector: {
          matchLabels: Record<string, string>;
        };
      };
    };
    provider: { name: string; version: string };
    controllerName: string;
    identityRef: {
      name: string;
    };
    parameters: {
      zoneCreationPolicy: "Create";
      zoneDeletionPolicy: "Delete";
    };
  };
}

interface Zone extends K8sResource {
  apiVersion: "dns.appthrust.io/v1alpha1";
  kind: "Zone";
  spec: {
    domainName: string;
    provider: { name: string; version: string };
    zoneClassRef: {
      namespace: string;
      name: string;
    };
  };
  status?: {
    nameServers?: Array<string>;
    provider?: {
      data?: {
        zone?: {
          id?: string;
          type?: string;
        };
      };
    };
    conditions?: Array<Condition>;
  };
}

interface RecordSet extends K8sResource {
  apiVersion: "dns.appthrust.io/v1alpha1";
  kind: "RecordSet";
  spec: RecordSetSpec;
  status?: {
    provider?: {
      data?: {
        records?: Array<CloudflareDNSRecordStatus>;
      };
    };
    conditions?: Array<Condition>;
  };
}

export interface RecordSetSpec {
  zoneRef: {
    name: string;
  };
  provider: { name: string; version: string };
  type: RecordType;
  name: string;
  ttl?: number;
  a?: {
    addresses: string[];
  };
  aaaa?: {
    addresses: string[];
  };
  txt?: {
    values: string[];
  };
  cname?: {
    target: string;
  };
  mx?: {
    records: Array<{ preference: number; exchange: string }>;
  };
  caa?: {
    records: Array<{ flags: number; tag: string; value: string }>;
  };
  ns?: {
    nameServers: string[];
  };
  options?: {
    ttl?: "Auto";
    proxied?: boolean;
    comment?: string;
    tags?: string[];
  };
}

interface CloudflareZoneResult {
  id: string;
  name: string;
  status: string;
  type: string;
  account: {
    id: string;
  };
}

interface CloudflareDNSRecordStatus {
  id: string;
  type?: string;
  name?: string;
  content?: string;
  priority?: number;
  ttl?: number;
  proxied?: boolean;
  comment?: string;
  tags?: string[];
}

interface CloudflareDNSRecordResult {
  id: string;
  type: string;
  name: string;
  content: string;
  priority?: number;
  ttl?: number;
  proxied?: boolean;
  comment?: string;
  tags?: string[];
  data?: {
    flags?: number;
    tag?: string;
    value?: string;
  };
}

interface CloudflareAPIResponseBody<T = unknown> {
  success?: boolean;
  result?: T;
  errors?: Array<{ code?: number; message?: string }>;
}

export interface ExpectedCloudflareRecord {
  type: RecordType;
  name: string;
  content: string;
  ttl: number;
  proxied?: boolean;
  priority?: number;
  caa?: {
    flags: number;
    tag: string;
    value: string;
  };
  normalizeContent?: "dns-name" | "ipv6" | "txt";
  comment?: string;
  tags?: string[];
  optionalMetadata?: boolean;
}

export interface CloudflareRecordSetScenario {
  title: string;
  resourceName: string;
  initial: {
    spec: RecordSetSpec;
    expected: ExpectedCloudflareRecord[];
  };
  update?: {
    spec: RecordSetSpec;
    expected: ExpectedCloudflareRecord[];
  };
  allowProviderInvalidRequest?: boolean;
}

export async function runCloudflareRecordSetLifecycle(
  s: Scenario,
  scenario: CloudflareRecordSetScenario,
) {
  s.given("platform and application namespaces exist");
  const platform = await s.newNamespace({
    generateName: "dns-api-cloudflare-platform-",
  });
  const app = await s.newNamespace({
    generateName: "dns-api-cloudflare-app-",
  });
  const testID = s.generateName("rs-");
  const domainName = await generateCloudflareTestDomainName(testID, cloudflareZoneNameExists);

  await s.label(
    {
      apiVersion: "v1",
      kind: "Namespace",
      name: app.name,
      labels: {
        "appthrust.io/cloudflare-test": testID,
      },
      overwrite: true,
    },
    { timeout: "30s" },
  );

  s.given("a CloudflareIdentity, ZoneClass, and Zone are ready");
  await createCloudflareZoneSetup(s, platform, app, testID, domainName);
  const zone = await app.assert<Zone>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "Zone",
      name: "cloudflare-zone",
      test() {
        expect(this.status?.conditions).toContainEqual(
          expect.objectContaining({
            type: "Accepted",
            status: "True",
          }),
        );
        expect(this.status?.conditions).toContainEqual(
          expect.objectContaining({
            type: "Programmed",
            status: "True",
          }),
        );
        expect(typeof this.status?.provider?.data?.zone?.id).toBe("string");
      },
    },
    waitForCloudflare,
  );
  const zoneID = requireString(zone.status?.provider?.data?.zone?.id);

  s.then("the Cloudflare zone exists in the selected account");
  await s.exec(
    {
      do: async () => {
        const observed = await getCloudflareZone(zoneID);
        expect(observed.account.id).toBe(cloudflareAccountID);
        expect(observed.name).toBe(domainName);
        expect(observed.type).toBe("full");
      },
    },
    waitForCloudflare,
  );

  s.when(`${scenario.title} is created by the application`);
  await applyRecordSet(app, scenario.resourceName, scenario.initial.spec);

  const initial = await waitForRecordSetState(
    app,
    scenario.resourceName,
    scenario.initial.expected.length,
    scenario.allowProviderInvalidRequest,
  );
  if (initial.providerInvalidRequest) {
    await deleteRecordSetAndZone(s, app, scenario.resourceName, zoneID, []);
    return;
  }
  const observedIDs = new Set(initial.recordIDs);
  await assertCloudflareRecordsForSpec(
    s,
    zoneID,
    scenario.initial.spec,
    domainName,
    scenario.initial.expected,
  );

  if (scenario.update) {
    s.when(`${scenario.title} is updated by the application`);
    await applyRecordSet(app, scenario.resourceName, scenario.update.spec);
    const updated = await waitForRecordSetState(
      app,
      scenario.resourceName,
      scenario.update.expected.length,
      false,
    );
    for (const staleID of [...observedIDs].filter((id) => !updated.recordIDs.includes(id))) {
      await assertCloudflareDNSRecordAbsentWithRetry(s, zoneID, staleID);
    }
    for (const recordID of updated.recordIDs) {
      observedIDs.add(recordID);
    }
    await assertCloudflareRecordsForSpec(
      s,
      zoneID,
      scenario.update.spec,
      domainName,
      scenario.update.expected,
    );
  }

  await deleteRecordSetAndZone(s, app, scenario.resourceName, zoneID, [...observedIDs]);
}

export function baseSpec(
  type: RecordType,
  name: string,
  patch: Omit<RecordSetSpec, "zoneRef" | "provider" | "type" | "name">,
): RecordSetSpec {
  return {
    zoneRef: {
      name: "cloudflare-zone",
    },
    provider: cloudflareProvider,
    type,
    name,
    ...patch,
  };
}

export function recordName(ownerName: string, domainName: string): string {
  return ownerName === "@" ? domainName : `${ownerName}.${domainName}`;
}

async function createCloudflareZoneSetup(
  s: Scenario,
  platform: Namespace,
  app: Namespace,
  testID: string,
  domainName: string,
) {
  await s.exec({
    do: async () => {
      await applyCloudflareTokenSecret(platform.name, "cloudflare-api-token", "api-token");
    },
  });
  await platform.apply<CloudflareIdentity>({
    apiVersion: "cloudflare.dns.appthrust.io/v1alpha1",
    kind: "CloudflareIdentity",
    metadata: { name: "cloudflare-local" },
    spec: {
      accessToken: {
        secretRef: {
          name: "cloudflare-api-token",
          key: "api-token",
        },
      },
    },
  });

  await platform.assert<CloudflareIdentity>(
    {
      apiVersion: "cloudflare.dns.appthrust.io/v1alpha1",
      kind: "CloudflareIdentity",
      name: "cloudflare-local",
      test() {
        expect(this.status?.account?.id).toBe(cloudflareAccountID);
        expect(this.status?.conditions).toContainEqual(
          expect.objectContaining({
            type: "Accepted",
            status: "True",
          }),
        );
        expect(this.status?.conditions).toContainEqual(
          expect.objectContaining({
            type: "Ready",
            status: "True",
          }),
        );
      },
    },
    waitForCloudflare,
  );

  await platform.apply<ZoneClass>({
    apiVersion: "dns.appthrust.io/v1alpha1",
    kind: "ZoneClass",
    metadata: { name: "cloudflare-public" },
    spec: {
      allowedZones: {
        namespaces: {
          from: "Selector",
          selector: {
            matchLabels: {
              "appthrust.io/cloudflare-test": testID,
            },
          },
        },
      },
      provider: cloudflareProvider,
        controllerName: "cloudflare.dns.appthrust.io/controller",
      identityRef: {
        name: "cloudflare-local",
      },
      parameters: {
        zoneCreationPolicy: "Create",
        zoneDeletionPolicy: "Delete",
      },
    },
  });

  await app.apply<Zone>({
    apiVersion: "dns.appthrust.io/v1alpha1",
    kind: "Zone",
    metadata: { name: "cloudflare-zone" },
    spec: {
      domainName,
      provider: cloudflareProvider,
      zoneClassRef: {
        namespace: platform.name,
        name: "cloudflare-public",
      },
    },
  });
}

async function applyRecordSet(app: Namespace, name: string, spec: RecordSetSpec) {
  await app.apply<RecordSet>({
    apiVersion: "dns.appthrust.io/v1alpha1",
    kind: "RecordSet",
    metadata: { name },
    spec,
  });
}

async function waitForRecordSetState(
  app: Namespace,
  name: string,
  expectedRecordCount: number,
  allowProviderInvalidRequest = false,
) {
  const recordSet = await app.assert<RecordSet>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "RecordSet",
      name,
      test() {
        expect(this.status?.conditions).toContainEqual(
          expect.objectContaining({
            type: "Accepted",
            status: "True",
          }),
        );
        const programmed = conditionOf(this.status?.conditions, "Programmed");
        if (programmed?.status === "True") {
          expect(recordIDsFromStatus(this.status?.provider?.data?.records)).toHaveLength(
            expectedRecordCount,
          );
          return;
        }
        if (allowProviderInvalidRequest) {
          expect(programmed).toEqual(
            expect.objectContaining({
              status: "False",
              reason: "ProviderInvalidRequest",
            }),
          );
          return;
        }
        expect(programmed).toEqual(
          expect.objectContaining({
            status: "True",
          }),
        );
      },
    },
    waitForCloudflare,
  );
  const programmed = conditionOf(recordSet.status?.conditions, "Programmed");
  if (programmed?.status === "False" && programmed.reason === "ProviderInvalidRequest") {
    return { providerInvalidRequest: true, recordIDs: [] as string[] };
  }
  return {
    providerInvalidRequest: false,
    recordIDs: recordIDsFromStatus(recordSet.status?.provider?.data?.records),
  };
}

async function assertCloudflareRecordsForSpec(
  s: Scenario,
  zoneID: string,
  spec: RecordSetSpec,
  domainName: string,
  expected: ExpectedCloudflareRecord[],
) {
  const fullName = recordName(spec.name, domainName);
  const expectedForDomain = expected.map((record) => ({
    ...record,
    name: record.name.replace("${domainName}", domainName),
  }));
  await s.exec(
    {
      do: async () => {
        const listed = await listCloudflareDNSRecords(zoneID, fullName, spec.type);
        assertCloudflareRecords(listed, expectedForDomain);
      },
    },
    waitForCloudflare,
  );
}

async function deleteRecordSetAndZone(
  s: Scenario,
  app: Namespace,
  recordSetName: string,
  zoneID: string,
  recordIDs: string[],
) {
  s.when("the RecordSet is deleted by the application");
  await app.delete<RecordSet>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "RecordSet",
      name: recordSetName,
    },
    waitForCloudflare,
  );
  await app.assertAbsence<RecordSet>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "RecordSet",
      name: recordSetName,
    },
    waitForCloudflare,
  );

  for (const recordID of recordIDs) {
    await assertCloudflareDNSRecordAbsentWithRetry(s, zoneID, recordID);
  }

  s.when("the Zone is deleted by the application");
  await app.delete<Zone>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "Zone",
      name: "cloudflare-zone",
    },
    waitForCloudflare,
  );
  await app.assertAbsence<Zone>(
    {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "Zone",
      name: "cloudflare-zone",
    },
    waitForCloudflare,
  );

  s.then("the Cloudflare zone is gone");
  await s.exec(
    {
      do: async () => {
        await assertCloudflareZoneAbsent(zoneID);
      },
    },
    waitForCloudflare,
  );
}

async function assertCloudflareDNSRecordAbsentWithRetry(
  s: Scenario,
  zoneID: string,
  recordID: string,
) {
  await s.exec(
    {
      do: async () => {
        await assertCloudflareDNSRecordAbsent(zoneID, recordID);
      },
    },
    waitForCloudflare,
  );
}

function assertCloudflareRecords(
  observed: CloudflareDNSRecordResult[],
  expected: ExpectedCloudflareRecord[],
) {
  expect(observed).toHaveLength(expected.length);
  const observedKeys = observed.map((record) => recordKey(record)).sort();
  const expectedKeys = expected.map((record) => expectedRecordKey(record)).sort();
  expect(observedKeys).toEqual(expectedKeys);
  for (const expectedRecord of expected) {
    const record = observed.find((item) => recordKey(item) === expectedRecordKey(expectedRecord));
    expect(record).toBeDefined();
    if (!record) {
      continue;
    }
    expect(record.ttl).toBe(expectedRecord.ttl);
    if (expectedRecord.proxied !== undefined) {
      expect(record.proxied).toBe(expectedRecord.proxied);
    }
    if (expectedRecord.comment !== undefined) {
      if (!expectedRecord.optionalMetadata || record.comment) {
        expect(record.comment).toBe(expectedRecord.comment);
      }
    }
    if (expectedRecord.tags !== undefined) {
      const tags = record.tags ?? [];
      if (!expectedRecord.optionalMetadata || tags.length > 0) {
        expect([...tags].sort()).toEqual([...expectedRecord.tags].sort());
      }
    }
  }
}

function recordKey(record: CloudflareDNSRecordResult): string {
  return [
    record.type,
    normalizeDNSName(record.name),
    normalizeContent(record.type, record.content, record),
    record.priority ?? "",
    record.data?.flags ?? "",
    record.data?.tag ?? "",
    record.data?.value ?? "",
  ].join("\0");
}

function expectedRecordKey(record: ExpectedCloudflareRecord): string {
  return [
    record.type,
    normalizeDNSName(record.name),
    normalizeExpectedContent(record),
    record.priority ?? "",
    record.caa?.flags ?? "",
    record.caa?.tag ?? "",
    record.caa?.value ?? "",
  ].join("\0");
}

function normalizeContent(
  type: string,
  content: string,
  record: CloudflareDNSRecordResult,
): string {
  if (type === "CAA" && record.data?.value) {
    return record.data.value;
  }
  if (type === "CNAME" || type === "NS" || type === "MX") {
    return normalizeDNSName(content);
  }
  if (type === "TXT") {
    return normalizeTXT(content);
  }
  if (type === "AAAA") {
    return normalizeIPv6(content);
  }
  return content;
}

function normalizeExpectedContent(record: ExpectedCloudflareRecord): string {
  if (record.normalizeContent === "dns-name") {
    return normalizeDNSName(record.content);
  }
  if (record.normalizeContent === "txt") {
    return normalizeTXT(record.content);
  }
  if (record.normalizeContent === "ipv6") {
    return normalizeIPv6(record.content);
  }
  return record.content;
}

function normalizeDNSName(value: string): string {
  return value.toLowerCase().replace(/\.$/, "");
}

function normalizeTXT(value: string): string {
  return value.replace(/^"|"$/g, "");
}

function normalizeIPv6(value: string): string {
  const lower = value.toLowerCase();
  const [left, right = ""] = lower.split("::");
  const leftParts = left ? left.split(":") : [];
  const rightParts = right ? right.split(":") : [];
  const missing = 8 - leftParts.length - rightParts.length;
  const parts = [...leftParts, ...Array(Math.max(missing, 0)).fill("0"), ...rightParts];
  return parts.map((part) => parseInt(part || "0", 16).toString(16)).join(":");
}

function recordIDsFromStatus(records?: Array<CloudflareDNSRecordStatus>): string[] {
  return (
    records
      ?.map((record) => record.id)
      .filter((id): id is string => typeof id === "string" && id.length > 0) ?? []
  );
}

function conditionOf(conditions: Array<Condition> | undefined, type: string) {
  return conditions?.find((condition) => condition.type === type);
}

async function applyCloudflareTokenSecret(namespace: string, name: string, key: string) {
  const tokenData = Buffer.from(cloudflareAPIToken, "utf8").toString("base64");
  const manifest = [
    "apiVersion: v1",
    "kind: Secret",
    "metadata:",
    `  namespace: ${namespace}`,
    `  name: ${name}`,
    "type: Opaque",
    "data:",
    `  ${key}: ${tokenData}`,
    "",
  ].join("\n");
  const proc = Bun.spawn(["kubectl", "apply", "-f", "-"], {
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  });
  proc.stdin.write(manifest);
  proc.stdin.end();
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  if (exitCode !== 0) {
    throw new Error(stderr || stdout || `kubectl apply exited with ${exitCode}`);
  }
}

async function getCloudflareZone(zoneID: string): Promise<CloudflareZoneResult> {
  const body = await cloudflareFetch<CloudflareZoneResult>(
    `/zones/${encodeURIComponent(zoneID)}`,
  );
  return body.result;
}

async function cloudflareZoneNameExists(name: string): Promise<boolean> {
  const params = new URLSearchParams({
    "account.id": cloudflareAccountID,
    name,
    per_page: "1",
  });
  const body = await cloudflareFetch<CloudflareZoneResult[]>(`/zones?${params}`);
  return body.result.some((zone) => zone.name === name);
}

async function assertCloudflareZoneAbsent(zoneID: string) {
  const response = await rawCloudflareFetch(`/zones/${encodeURIComponent(zoneID)}`);
  if (isCloudflareNotFound(response)) {
    return;
  }
  if (!response.ok) {
    throw new Error(`Cloudflare API returned HTTP ${response.status}`);
  }
  throw new Error(`Cloudflare zone ${zoneID} still exists`);
}

async function listCloudflareDNSRecords(zoneID: string, name: string, type: RecordType) {
  const query = new URLSearchParams({ name, type });
  const body = await cloudflareFetch<CloudflareDNSRecordResult[]>(
    `/zones/${encodeURIComponent(zoneID)}/dns_records?${query.toString()}`,
  );
  return body.result;
}

async function assertCloudflareDNSRecordAbsent(zoneID: string, recordID: string) {
  const response = await rawCloudflareFetch(
    `/zones/${encodeURIComponent(zoneID)}/dns_records/${encodeURIComponent(recordID)}`,
  );
  if (isCloudflareNotFound(response)) {
    return;
  }
  if (!response.ok) {
    throw new Error(`Cloudflare API returned HTTP ${response.status}`);
  }
  throw new Error(`Cloudflare DNS record ${recordID} still exists`);
}

async function cloudflareFetch<T>(path: string): Promise<{
  success: boolean;
  result: T;
  errors?: Array<{ code?: number; message?: string }>;
}> {
  const response = await rawCloudflareFetch(path);
  const body = response.body;
  if (!response.ok || body.success === false) {
    const message = body.errors?.[0]?.message ?? `Cloudflare API returned HTTP ${response.status}`;
    throw new Error(message);
  }
  return body as {
    success: boolean;
    result: T;
    errors?: Array<{ code?: number; message?: string }>;
  };
}

async function rawCloudflareFetch(path: string) {
  const response = await fetch(`https://api.cloudflare.com/client/v4${path}`, {
    headers: {
      Authorization: `Bearer ${cloudflareAPIToken}`,
      Accept: "application/json",
    },
  });
  const body = (await response.json()) as CloudflareAPIResponseBody;
  return { ok: response.ok, status: response.status, body };
}

function isCloudflareNotFound(response: {
  ok: boolean;
  status: number;
  body: { errors?: Array<{ code?: number; message?: string }> };
}) {
  const error = response.body.errors?.[0];
  const message = error?.message ?? "";
  return (
    response.status === 404 ||
    error?.code === 1001 ||
    error?.code === 81044 ||
    message.includes("Invalid zone identifier") ||
    message.includes("Record does not exist") ||
    message.toLowerCase().includes("not found")
  );
}

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function requireString(value: unknown): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error("Expected a non-empty string");
  }
  return value;
}
