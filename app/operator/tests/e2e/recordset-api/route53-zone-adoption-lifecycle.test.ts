import {
  CreateHostedZoneCommand,
  DeleteHostedZoneCommand,
  GetHostedZoneCommand,
  ListTagsForResourceCommand,
  Route53Client,
} from "@aws-sdk/client-route-53";
import { type K8sResource, test } from "@appthrust/kest";
import { expect } from "bun:test";

if (!process.env.AWS_PROFILE && process.env.PROFILE) {
  process.env.AWS_PROFILE = process.env.PROFILE;
}
process.env.AWS_REGION ??= "ap-northeast-1";

const awsRegion = process.env.AWS_REGION ?? "ap-northeast-1";
const route53 = new Route53Client({ region: awsRegion });
const route53AccountID = process.env.ROUTE53_ACCOUNT_ID;
if (!route53AccountID) {
  throw new Error("ROUTE53_ACCOUNT_ID is required");
}
const waitForRoute53 = {
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

interface Route53Identity extends K8sResource {
  apiVersion: "route53.dns.appthrust.io/v1alpha1";
  kind: "Route53Identity";
  spec: {
    accountID: string;
    region: string;
    credentials: {
      runtime: Record<string, never>;
    };
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
      zoneCreationPolicy: "Deny";
      zoneDeletionPolicy: "Retain";
      sameNameZonePolicy: "Deny";
      tags: Record<string, string>;
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
    adoption: {
      hostedZoneId: string;
    };
  };
  status?: {
    provider?: {
      data?: {
        hostedZoneID?: string;
      };
    };
    nameServers?: Array<string>;
    conditions?: Array<Condition>;
  };
}

test(
  "a Route 53 Zone adopts an existing hosted zone and retains it on deletion",
  async (s) => {
    s.given("an existing Route 53 hosted zone is outside Kubernetes");
    const testID = s.generateName("hz-");
    const domainName = `${testID}.dns-api.test`;
    const hostedZoneID = await createHostedZone(domainName, testID);
    try {
      s.given("platform and application namespaces exist");
      const platform = await s.newNamespace({
        generateName: "dns-api-platform-",
      });
      const app = await s.newNamespace({ generateName: "dns-api-app-" });

      await s.label(
        {
          apiVersion: "v1",
          kind: "Namespace",
          name: app.name,
          labels: {
            "appthrust.io/tenant": testID,
          },
          overwrite: true,
        },
        { timeout: "30s" },
      );

      s.given("an adoption-only Route53Identity and ZoneClass allow the app");
      await platform.apply<Route53Identity>({
        apiVersion: "route53.dns.appthrust.io/v1alpha1",
        kind: "Route53Identity",
        metadata: { name: "route53-dev" },
        spec: {
          accountID: route53AccountID,
          region: awsRegion,
          credentials: {
            runtime: {},
          },
        },
      });
      await platform.apply<ZoneClass>({
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "ZoneClass",
        metadata: { name: "route53-adoption" },
        spec: {
          allowedZones: {
            namespaces: {
              from: "Selector",
              selector: {
                matchLabels: {
                  "appthrust.io/tenant": testID,
                },
              },
            },
          },
          provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
          controllerName: "route53.dns.appthrust.io/controller",
          identityRef: {
            name: "route53-dev",
          },
          parameters: {
            zoneCreationPolicy: "Deny",
            zoneDeletionPolicy: "Retain",
            sameNameZonePolicy: "Deny",
            tags: {
              "appthrust.io/test-scope": "kest",
              "appthrust.io/test-id": testID,
            },
          },
        },
      });

      s.when("a Zone explicitly adopts the hosted zone");
      await app.apply<Zone>({
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "Zone",
        metadata: { name: "apps-example-com" },
        spec: {
          domainName,
          provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
          zoneClassRef: {
            namespace: platform.name,
            name: "route53-adoption",
          },
          adoption: {
            hostedZoneId: hostedZoneID,
          },
        },
      });

      s.then("the Zone reports the adopted hosted zone as programmed");
      await app.assert<Zone>(
        {
          apiVersion: "dns.appthrust.io/v1alpha1",
          kind: "Zone",
          name: "apps-example-com",
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
            expect(this.status?.provider?.data?.hostedZoneID).toBe(
              hostedZoneID,
            );
            expect(this.status?.nameServers?.length).toBeGreaterThan(0);
          },
        },
        waitForRoute53,
      );

      s.then("Route 53 has dns-api management tags on the adopted zone");
      await s.exec(
        {
          do: async () => {
            const tags = await getHostedZoneTags(hostedZoneID);
            expect(tags["appthrust.io/managed-by"]).toBe("dns-api");
            expect(tags["appthrust.io/test-scope"]).toBe("kest");
            expect(tags["appthrust.io/test-id"]).toBe(testID);
          },
        },
        waitForRoute53,
      );

      s.when("the adopted Zone is deleted");
      await app.delete<Zone>(
        {
          apiVersion: "dns.appthrust.io/v1alpha1",
          kind: "Zone",
          name: "apps-example-com",
        },
        waitForRoute53,
      );
      await app.assertAbsence<Zone>(
        {
          apiVersion: "dns.appthrust.io/v1alpha1",
          kind: "Zone",
          name: "apps-example-com",
        },
        waitForRoute53,
      );

      s.then("the Route 53 hosted zone is retained by policy");
      await s.exec(
        {
          do: async () => {
            await assertHostedZoneExists(hostedZoneID, domainName);
          },
        },
        waitForRoute53,
      );
    } finally {
      await deleteHostedZoneIfExists(hostedZoneID);
    }
  },
  { timeout: "20m" },
);

async function createHostedZone(domainName: string, callerReference: string) {
  const output = await route53.send(
    new CreateHostedZoneCommand({
      Name: domainName,
      CallerReference: `dns-api-adoption:${callerReference}`,
    }),
  );
  return requireString(output.HostedZone?.Id).replace(/^\/hostedzone\//, "");
}

async function assertHostedZoneExists(
  hostedZoneID: string,
  domainName: string,
) {
  const output = await route53.send(
    new GetHostedZoneCommand({ Id: hostedZoneID }),
  );
  expect(output.HostedZone?.Name).toBe(`${domainName}.`);
}

async function getHostedZoneTags(hostedZoneID: string) {
  const output = await route53.send(
    new ListTagsForResourceCommand({
      ResourceId: hostedZoneID,
      ResourceType: "hostedzone",
    }),
  );
  return Object.fromEntries(
    output.ResourceTagSet?.Tags?.map((tag) => [tag.Key, tag.Value]) ?? [],
  );
}

async function deleteHostedZoneIfExists(hostedZoneID: string) {
  try {
    await route53.send(new DeleteHostedZoneCommand({ Id: hostedZoneID }));
  } catch (error) {
    if (awsErrorName(error) === "NoSuchHostedZone") {
      return;
    }
    throw error;
  }
}

function awsErrorName(error: unknown): string | undefined {
  if (typeof error !== "object" || error === null || !("name" in error)) {
    return undefined;
  }
  return String(error.name);
}

function requireString(value: unknown): string {
  expect(value).toBeString();
  return value as string;
}
