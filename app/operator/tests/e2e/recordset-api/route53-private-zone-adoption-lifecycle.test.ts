import {
  ChangeResourceRecordSetsCommand,
  CreateHostedZoneCommand,
  DeleteHostedZoneCommand,
  GetHostedZoneCommand,
  ListResourceRecordSetsCommand,
  Route53Client,
  type VPCRegion,
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
const privateZoneVPCID = process.env.ROUTE53_PRIVATE_ZONE_VPC_ID;
const cnameTarget = "target.example.net";
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
      zoneType: "Private";
      zoneCreationPolicy: "Deny" | "Create";
      zoneDeletionPolicy: "Retain";
      sameNameZonePolicy: "Deny";
      tags: Record<string, string>;
    };
  };
  status?: {
    conditions?: Array<Condition>;
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
        zoneType?: "Public" | "Private";
      };
    };
    nameServers?: Array<string>;
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
    type: "CNAME";
    name: string;
    ttl: number;
    cname: {
      target: string;
    };
  };
  status?: {
    conditions?: Array<Condition>;
  };
}

test(
  "a Route 53 Zone adopts an existing private hosted zone, programs a CNAME, and retains it on deletion",
  async (s) => {
    if (!privateZoneVPCID) {
      console.log(
        "Skipping Route 53 private hosted zone adoption: ROUTE53_PRIVATE_ZONE_VPC_ID is unset",
      );
      return;
    }
    if (!route53AccountID) {
      throw new Error("ROUTE53_ACCOUNT_ID is required");
    }

    s.given("an existing Route 53 private hosted zone is outside Kubernetes");
    const testID = s.generateName("hz-");
    const domainName = `${testID}.dns-api.test`;
    const hostedZoneID = await createHostedZone(domainName, testID, privateZoneVPCID);
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

      s.given("an adoption-only private Route53Identity and ZoneClass allow the app");
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
        metadata: { name: "route53-private-adoption" },
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
            zoneType: "Private",
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

      s.when("a Zone explicitly adopts the private hosted zone");
      await app.apply<Zone>({
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "Zone",
        metadata: { name: "apps-example-com" },
        spec: {
          domainName,
          provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
          zoneClassRef: {
            namespace: platform.name,
            name: "route53-private-adoption",
          },
          adoption: {
            hostedZoneId: hostedZoneID,
          },
        },
      });

      s.then("the Zone reports the private hosted zone as programmed without name servers");
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
            expect(this.status?.provider?.data?.zoneType).toBe("Private");
            expect(this.status?.nameServers ?? []).toEqual([]);
          },
        },
        waitForRoute53,
      );

      s.when("a CNAME RecordSet is created by the application");
      await app.apply<RecordSet>({
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "RecordSet",
        metadata: { name: "www-cname" },
        spec: {
          zoneRef: {
            name: "apps-example-com",
          },
          provider: { name: "route53.dns.appthrust.io", version: "v1alpha1" },
          type: "CNAME",
          name: "www",
          ttl: 300,
          cname: {
            target: cnameTarget,
          },
        },
      });
      try {
        s.then("the RecordSet reports Accepted=True and Programmed=True");
        await app.assert<RecordSet>(
          {
            apiVersion: "dns.appthrust.io/v1alpha1",
            kind: "RecordSet",
            name: "www-cname",
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

        s.then("the Route 53 CNAME record set exists in the private hosted zone");
        await s.exec(
          {
            do: async () => {
              await assertCNAMERecordSetExists(hostedZoneID, domainName);
            },
          },
          waitForRoute53,
        );
      } finally {
        s.when("the RecordSet is deleted by the application");
        await app.delete<RecordSet>(
          {
            apiVersion: "dns.appthrust.io/v1alpha1",
            kind: "RecordSet",
            name: "www-cname",
          },
          waitForRoute53,
        );
        await app.assertAbsence<RecordSet>(
          {
            apiVersion: "dns.appthrust.io/v1alpha1",
            kind: "RecordSet",
            name: "www-cname",
          },
          waitForRoute53,
        );
      }

      s.then("the Route 53 CNAME record set is gone");
      await s.exec(
        {
          do: async () => {
            await assertCNAMERecordSetAbsent(hostedZoneID, domainName);
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

      s.then("the Route 53 private hosted zone is retained by policy");
      await s.exec(
        {
          do: async () => {
            await assertHostedZoneExists(hostedZoneID, domainName);
          },
        },
        waitForRoute53,
      );
    } finally {
      await s.exec(
        {
          do: async () => {
            await deleteHostedZoneIfExists(hostedZoneID, domainName);
          },
        },
        waitForRoute53,
      );
    }
  },
  { timeout: "20m" },
);

test(
  "a Route 53 private ZoneClass rejects hosted zone creation",
  async (s) => {
    if (!privateZoneVPCID) {
      console.log(
        "Skipping Route 53 private ZoneClass validation: ROUTE53_PRIVATE_ZONE_VPC_ID is unset",
      );
      return;
    }
    if (!route53AccountID) {
      throw new Error("ROUTE53_ACCOUNT_ID is required");
    }

    s.given("a platform namespace and Route53Identity exist");
    const platform = await s.newNamespace({
      generateName: "dns-api-platform-",
    });
    const testID = s.generateName("hz-");
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

    s.when("a private ZoneClass allows hosted zone creation");
    await platform.apply<ZoneClass>({
      apiVersion: "dns.appthrust.io/v1alpha1",
      kind: "ZoneClass",
      metadata: { name: "route53-private-create" },
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
          zoneType: "Private",
          zoneCreationPolicy: "Create",
          zoneDeletionPolicy: "Retain",
          sameNameZonePolicy: "Deny",
          tags: {
            "appthrust.io/test-scope": "kest",
            "appthrust.io/test-id": testID,
          },
        },
      },
    });

    s.then("the ZoneClass reports Accepted=False with InvalidParameters");
    await platform.assert<ZoneClass>(
      {
        apiVersion: "dns.appthrust.io/v1alpha1",
        kind: "ZoneClass",
        name: "route53-private-create",
        test() {
          expect(this.status?.conditions).toContainEqual(
            expect.objectContaining({
              type: "Accepted",
              status: "False",
              reason: "InvalidParameters",
            }),
          );
        },
      },
      waitForRoute53,
    );
  },
  { timeout: "20m" },
);

async function createHostedZone(
  domainName: string,
  callerReference: string,
  vpcID: string,
) {
  const output = await route53.send(
    new CreateHostedZoneCommand({
      Name: domainName,
      CallerReference: `dns-api-private-adoption:${callerReference}`,
      HostedZoneConfig: {
        PrivateZone: true,
      },
      VPC: {
        VPCRegion: awsRegion as VPCRegion,
        VPCId: vpcID,
      },
    }),
  );
  return requireString(output.HostedZone?.Id).replace(/^\/hostedzone\//, "");
}

async function assertHostedZoneExists(hostedZoneID: string, domainName: string) {
  const output = await route53.send(new GetHostedZoneCommand({ Id: hostedZoneID }));
  expect(output.HostedZone?.Name).toBe(`${domainName}.`);
  expect(output.HostedZone?.Config?.PrivateZone).toBe(true);
}

async function assertCNAMERecordSetExists(hostedZoneID: string, domainName: string) {
  const record = await getCNAMERecordSet(hostedZoneID, domainName);
  expect(record?.Name).toBe(`www.${domainName}.`);
  expect(record?.Type).toBe("CNAME");
  expect(record?.TTL).toBe(300);
  expect(record?.ResourceRecords?.map((item) => item.Value)).toEqual([`${cnameTarget}.`]);
}

async function assertCNAMERecordSetAbsent(hostedZoneID: string, domainName: string) {
  const record = await getCNAMERecordSet(hostedZoneID, domainName);
  expect(record).toBeUndefined();
}

async function getCNAMERecordSet(hostedZoneID: string, domainName: string) {
  const recordName = `www.${domainName}.`;
  const output = await route53.send(
    new ListResourceRecordSetsCommand({
      HostedZoneId: hostedZoneID,
      StartRecordName: recordName,
      StartRecordType: "CNAME",
      MaxItems: 1,
    }),
  );
  const record = output.ResourceRecordSets?.[0];
  if (record?.Name !== recordName || record.Type !== "CNAME") {
    return undefined;
  }
  return record;
}

async function deleteHostedZoneIfExists(hostedZoneID: string, domainName: string) {
  try {
    // A failed assertion or controller cleanup can leave the test CNAME behind.
    const record = await getCNAMERecordSet(hostedZoneID, domainName);
    if (record) {
      await route53.send(
        new ChangeResourceRecordSetsCommand({
          HostedZoneId: hostedZoneID,
          ChangeBatch: {
            Changes: [{ Action: "DELETE", ResourceRecordSet: record }],
          },
        }),
      );
    }
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
