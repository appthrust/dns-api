import {
  GetHostedZoneCommand,
  ListResourceRecordSetsCommand,
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
const txtValues = ["challenge-token", "v=spf1 include:_spf.example.net ~all"];

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
      zoneCreationPolicy: "Create";
      zoneDeletionPolicy: "Delete";
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
  };
  status?: {
    provider?: {
      data?: {
        hostedZoneID?: string;
      };
    };
    conditions?: Array<Condition>;
  };
}

interface RecordSet extends K8sResource {
  apiVersion: "dns.appthrust.io/v1alpha1";
  kind: "RecordSet";
  spec: {
    zoneRef: {
      name: string;
    };
    provider: { name: string; version: string };
    type: "TXT";
    name: string;
    ttl: number;
    txt: {
      values: Array<string>;
    };
  };
  status?: {
    conditions?: Array<Condition>;
  };
}

test(
  "a Route 53 TXT RecordSet follows the Zone lifecycle",
  async (s) => {
    s.given("platform and application namespaces exist");
    const platform = await s.newNamespace({
      generateName: "dns-api-platform-",
    });
    const app = await s.newNamespace({ generateName: "dns-api-app-" });
    const testID = s.generateName("hz-");
    const domainName = `${testID}.dns-api.test`;

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

    s.given("a Route53Identity and ZoneClass allow the application namespace");
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
      metadata: { name: "route53-public" },
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
          zoneCreationPolicy: "Create",
          zoneDeletionPolicy: "Delete",
          sameNameZonePolicy: "Deny",
          tags: {
            "appthrust.io/test-scope": "kest",
            "appthrust.io/test-id": testID,
          },
        },
      },
    });

    s.when("a Zone is created by the application");
    await app.apply<Zone>({
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "Zone",
      metadata: { name: "apps-example-com" },
      spec: {
        domainName,
        provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
        zoneClassRef: {
          namespace: platform.name,
          name: "route53-public",
        },
      },
    });

    s.then("the Zone reports Accepted=True and Programmed=True");
    const zone = await app.assert<Zone>(
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
          expect(this.status?.provider?.data?.hostedZoneID).toMatch(/^Z[A-Z0-9]+$/);
        },
      },
      waitForRoute53,
    );
    const hostedZoneID = requireString(zone.status?.provider?.data?.hostedZoneID);

    s.then("the Route 53 hosted zone exists");
    await s.exec(
      {
        do: async () => {
          await assertHostedZoneExists(hostedZoneID, domainName);
        },
      },
      waitForRoute53,
    );

    s.when("a TXT RecordSet is created by the application");
    await app.apply<RecordSet>({
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "RecordSet",
      metadata: { name: "acme-challenge" },
      spec: {
        zoneRef: {
          name: "apps-example-com",
        },
        provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
        type: "TXT",
        name: "_acme-challenge",
        ttl: 300,
        txt: {
          values: txtValues,
        },
      },
    });

    s.then("the RecordSet reports Accepted=True and Programmed=True");
    await app.assert<RecordSet>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        name: "acme-challenge",
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
        },
      },
      waitForRoute53,
    );

    s.then("the Route 53 TXT record set exists");
    await s.exec(
      {
        do: async () => {
          await assertTXTRecordSetExists(hostedZoneID, domainName);
        },
      },
      waitForRoute53,
    );

    s.when("the RecordSet is deleted by the application");
    await app.delete<RecordSet>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        name: "acme-challenge",
      },
      waitForRoute53,
    );
    await app.assertAbsence<RecordSet>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        name: "acme-challenge",
      },
      waitForRoute53,
    );

    s.then("the Route 53 TXT record set is gone");
    await s.exec(
      {
        do: async () => {
          await assertTXTRecordSetAbsent(hostedZoneID, domainName);
        },
      },
      waitForRoute53,
    );

    s.when("the Zone is deleted by the application");
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

    s.then("the Route 53 hosted zone is gone");
    await s.exec(
      {
        do: async () => {
          await assertHostedZoneAbsent(hostedZoneID);
        },
      },
      waitForRoute53,
    );
  },
  { timeout: "20m" },
);

async function assertHostedZoneExists(hostedZoneID: string, domainName: string) {
  const output = await route53.send(new GetHostedZoneCommand({ Id: hostedZoneID }));
  expect(output.HostedZone?.Name).toBe(`${domainName}.`);
}

async function assertHostedZoneAbsent(hostedZoneID: string) {
  try {
    await route53.send(new GetHostedZoneCommand({ Id: hostedZoneID }));
  } catch (error) {
    expect(awsErrorName(error)).toBe("NoSuchHostedZone");
    return;
  }
  throw new Error(`Route 53 hosted zone ${hostedZoneID} still exists`);
}

async function assertTXTRecordSetExists(hostedZoneID: string, domainName: string) {
  const record = await getTXTRecordSet(hostedZoneID, domainName);
  expect(record?.Name).toBe(`_acme-challenge.${domainName}.`);
  expect(record?.Type).toBe("TXT");
  expect(record?.TTL).toBe(300);
  const values = record?.ResourceRecords?.map((item) => parseTXTRecordValue(item.Value ?? ""));
  expect(values?.sort()).toEqual([...txtValues].sort());
}

async function assertTXTRecordSetAbsent(hostedZoneID: string, domainName: string) {
  const record = await getTXTRecordSet(hostedZoneID, domainName);
  expect(record).toBeUndefined();
}

async function getTXTRecordSet(hostedZoneID: string, domainName: string) {
  const recordName = `_acme-challenge.${domainName}.`;
  const output = await route53.send(
    new ListResourceRecordSetsCommand({
      HostedZoneId: hostedZoneID,
      StartRecordName: recordName,
      StartRecordType: "TXT",
      MaxItems: 1,
    }),
  );
  const record = output.ResourceRecordSets?.[0];
  if (record?.Name !== recordName || record.Type !== "TXT") {
    return undefined;
  }
  return record;
}

function parseTXTRecordValue(value: string): string {
  let output = "";
  let index = 0;
  while (index < value.length) {
    while (value[index] === " " || value[index] === "\t") {
      index++;
    }
    if (index >= value.length) {
      break;
    }
    expect(value[index]).toBe('"');
    index++;
    while (index < value.length && value[index] !== '"') {
      if (value[index] === "\\") {
        const escaped = value.slice(index + 1, index + 4);
        if (/^\d{3}$/.test(escaped)) {
          output += String.fromCharCode(Number(escaped));
          index += 4;
          continue;
        }
        index++;
      }
      output += value[index];
      index++;
    }
    expect(value[index]).toBe('"');
    index++;
  }
  return output;
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
