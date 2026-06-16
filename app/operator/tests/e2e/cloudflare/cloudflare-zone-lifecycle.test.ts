import { type K8sResource, test } from "@appthrust/kest";
import { expect } from "bun:test";
import { generateCloudflareTestDomainName } from "./cloudflare-test-names";

const cloudflareAccountID = requireEnv("CF_ACCOUNT_ID");
const cloudflareAPIToken = requireEnv("CF_API_TOKEN");
const cloudflareProvider = { name: "cloudflare.dns.appthrust.io", version: "v1alpha1" };
const waitForCloudflare = {
  timeout: "5m",
  interval: "5s",
  stallTimeout: "0s",
};

interface Condition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
  observedGeneration?: number;
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
          status?: string;
          type?: string;
        };
      };
    };
    conditions?: Array<Condition>;
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

interface CloudflareAPIResponseBody<T = unknown> {
  success?: boolean;
  result: T;
  errors?: Array<{ code?: number; message?: string }>;
}

test(
  "a Cloudflare Zone follows the Kubernetes Zone lifecycle",
  async (s) => {
    s.given("platform and application namespaces exist");
    const platform = await s.newNamespace({
      generateName: "dns-api-cloudflare-platform-",
    });
    const app = await s.newNamespace({
      generateName: "dns-api-cloudflare-app-",
    });
    const testID = s.generateName("zone-");
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

    s.given("a CloudflareIdentity and ZoneClass allow the application namespace");
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

    s.when("a Zone is created by the application");
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

    s.then("the Zone reports Accepted=True and Programmed=True");
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
          expect(this.status?.provider?.data?.zone?.type).toBe("full");
          expect(this.status?.nameServers?.length).toBeGreaterThan(0);
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
          expect(["pending", "active"]).toContain(observed.status);
        },
      },
      waitForCloudflare,
    );

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
  },
  { timeout: "15m" },
);

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
  const body = await cloudflareFetch<CloudflareZoneResult>(`/zones/${encodeURIComponent(zoneID)}`);
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
  const response = await fetch(
    `https://api.cloudflare.com/client/v4/zones/${encodeURIComponent(zoneID)}`,
    {
      headers: {
        Authorization: `Bearer ${cloudflareAPIToken}`,
        Accept: "application/json",
      },
    },
  );
  if (response.status === 404) {
    return;
  }
  const body = (await response.json()) as CloudflareAPIResponseBody;
  const error = body.errors?.[0];
  if (response.status === 400 && (error?.code === 1001 || error?.message?.includes("Invalid zone identifier"))) {
    return;
  }
  if (!response.ok) {
    throw new Error(`Cloudflare API returned HTTP ${response.status}`);
  }
  throw new Error(`Cloudflare zone ${zoneID} still exists`);
}

async function cloudflareFetch<T = unknown>(path: string): Promise<CloudflareAPIResponseBody<T>> {
  const response = await fetch(`https://api.cloudflare.com/client/v4${path}`, {
    headers: {
      Authorization: `Bearer ${cloudflareAPIToken}`,
      Accept: "application/json",
    },
  });
  const body = (await response.json()) as CloudflareAPIResponseBody<T>;
  if (!response.ok || body.success === false) {
    const message = body.errors?.[0]?.message ?? `Cloudflare API returned HTTP ${response.status}`;
    throw new Error(message);
  }
  return body;
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
