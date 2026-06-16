import {
  GetHostedZoneCommand,
  ListResourceRecordSetsCommand,
  Route53Client,
} from "@aws-sdk/client-route-53";
import { type K8sResource, type Namespace, test } from "@appthrust/kest";
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
    type: "AAAA";
    name: string;
    ttl: number;
    aaaa: {
      addresses: Array<string>;
    };
  };
  status?: {
    conditions?: Array<Condition>;
  };
}

test(
  "a Route 53 AAAA RecordSet follows create, update, and delete lifecycle",
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
          expect(this.status?.provider?.data?.hostedZoneID).toMatch(
            /^Z[A-Z0-9]+$/,
          );
        },
      },
      waitForRoute53,
    );
    const hostedZoneID = requireString(
      zone.status?.provider?.data?.hostedZoneID,
    );

    s.then("the Route 53 hosted zone exists");
    await s.exec(
      {
        do: async () => {
          await assertHostedZoneExists(hostedZoneID, domainName);
        },
      },
      waitForRoute53,
    );

    s.when("an AAAA RecordSet is created by the application");
    const recordSet: RecordSet = {
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "RecordSet",
      metadata: { name: "ipv6" },
      spec: {
        zoneRef: {
          name: "apps-example-com",
        },
        provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
        type: "AAAA",
        name: "ipv6",
        ttl: 300,
        aaaa: {
          addresses: ["2001:db8::10"],
        },
      },
    };
    await app.apply<RecordSet>(recordSet);

    s.then("the RecordSet reports Accepted=True and Programmed=True");
    await assertRecordSetProgrammed(app, "ipv6");

    s.then("the Route 53 AAAA record set exists");
    await s.exec(
      {
        do: async () => {
          await assertAAAARecordSet(hostedZoneID, domainName, {
            ttl: 300,
            addresses: ["2001:db8::10"],
          });
        },
      },
      waitForRoute53,
    );

    s.when("the AAAA RecordSet is updated by the application");
    const updatedRecordSet = structuredClone(recordSet);
    updatedRecordSet.spec.ttl = 600;
    updatedRecordSet.spec.aaaa.addresses = ["2001:db8::20"];
    await app.apply<RecordSet>(updatedRecordSet);

    s.then("the updated RecordSet reports Accepted=True and Programmed=True");
    await assertRecordSetProgrammed(app, "ipv6");

    s.then("the Route 53 AAAA record set is updated");
    await s.exec(
      {
        do: async () => {
          await assertAAAARecordSet(hostedZoneID, domainName, {
            ttl: 600,
            addresses: ["2001:db8::20"],
          });
        },
      },
      waitForRoute53,
    );

    s.when("the RecordSet is deleted by the application");
    await app.delete<RecordSet>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        name: "ipv6",
      },
      waitForRoute53,
    );
    await app.assertAbsence<RecordSet>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        name: "ipv6",
      },
      waitForRoute53,
    );

    s.then("the Route 53 AAAA record set is gone");
    await s.exec(
      {
        do: async () => {
          await assertAAAARecordSetAbsent(hostedZoneID, domainName);
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
  { timeout: "25m" },
);

async function assertRecordSetProgrammed(namespace: Namespace, name: string) {
  await namespace.assert<RecordSet>(
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

async function assertHostedZoneAbsent(hostedZoneID: string) {
  try {
    await route53.send(new GetHostedZoneCommand({ Id: hostedZoneID }));
  } catch (error) {
    expect(awsErrorName(error)).toBe("NoSuchHostedZone");
    return;
  }
  throw new Error(`Route 53 hosted zone ${hostedZoneID} still exists`);
}

async function assertAAAARecordSet(
  hostedZoneID: string,
  domainName: string,
  expected: { ttl: number; addresses: Array<string> },
) {
  const record = await getAAAARecordSet(hostedZoneID, domainName);
  expect(record).toMatchObject({
    Name: `ipv6.${domainName}.`,
    Type: "AAAA",
    TTL: expected.ttl,
    ResourceRecords: expected.addresses.map((address) => ({ Value: address })),
  });
}

async function assertAAAARecordSetAbsent(
  hostedZoneID: string,
  domainName: string,
) {
  const record = await getAAAARecordSet(hostedZoneID, domainName);
  expect(record).toBeUndefined();
}

async function getAAAARecordSet(hostedZoneID: string, domainName: string) {
  const recordName = `ipv6.${domainName}.`;
  const output = await route53.send(
    new ListResourceRecordSetsCommand({
      HostedZoneId: hostedZoneID,
      StartRecordName: recordName,
      StartRecordType: "AAAA",
      MaxItems: 1,
    }),
  );
  const record = output.ResourceRecordSets?.[0];
  if (record?.Name !== recordName || record.Type !== "AAAA") {
    return undefined;
  }
  return record;
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
