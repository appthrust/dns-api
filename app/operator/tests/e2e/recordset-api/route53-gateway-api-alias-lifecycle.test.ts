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

interface Condition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
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
    allowedRecordSets?: Array<{
      namespaces: {
        selector: {
          matchLabels: Record<string, string>;
        };
      };
      records: Array<{
        name: {
          pattern: string;
        };
        types: Array<"A" | "AAAA" | "CNAME">;
      }>;
    }>;
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

interface Gateway extends K8sResource {
  apiVersion: "gateway.networking.k8s.io/v1";
  kind: "Gateway";
  spec: {
    gatewayClassName: string;
    listeners: Array<{
      name: string;
      protocol: "HTTPS";
      port: number;
      hostname?: string;
      allowedRoutes?: {
        namespaces: {
          from: "All";
        };
      };
    }>;
  };
}

interface HTTPRoute extends K8sResource {
  apiVersion: "gateway.networking.k8s.io/v1";
  kind: "HTTPRoute";
  spec: {
    parentRefs: Array<{
      namespace: string;
      name: string;
      sectionName: string;
    }>;
    hostnames?: Array<string>;
    rules: Array<{
      backendRefs: Array<{
        name: string;
        port: number;
      }>;
    }>;
  };
}

interface EndpointRecordSet extends K8sResource {
  apiVersion: "endpoint.dns.appthrust.io/v1alpha1";
  kind: "EndpointRecordSet";
  status?: {
    hostnameCount?: number;
    hostnames?: Array<{
      hostname: string;
      zone?: {
        ref: {
          namespace: string;
          name: string;
        };
        domainName: string;
      };
      recordSets?: Array<{
        ref: {
          namespace: string;
          name: string;
        };
        type: "A" | "AAAA" | "CNAME";
        name: string;
        fragment?: {
          type: "A" | "AAAA" | "CNAME";
          name: string;
          options?: {
            alias?: {
              dnsName: string;
              hostedZoneID: string;
              evaluateTargetHealth: boolean;
            };
          };
        };
      }>;
      conditions?: Array<Condition>;
    }>;
    conditions?: Array<Condition>;
  };
}

test(
  "Gateway API HTTPRoute hostnames become Route 53 ALIAS A and AAAA RecordSets",
  async (s) => {
    s.given("Gateway API CRDs and the dns-api controller are installed");
    await s.exec({
      do: async ({ $ }) => {
        await $`kubectl get crd gateways.gateway.networking.k8s.io httproutes.gateway.networking.k8s.io endpointrecordsets.endpoint.dns.appthrust.io`;
        await $`kubectl -n dns-api-system get deploy dns-api-controller-manager`;
      },
    });

    s.given("platform and application namespaces exist");
    const platform = await s.newNamespace({
      generateName: "dns-api-platform-",
    });
    const app = await s.newNamespace({ generateName: "dns-api-app-" });
    const testID = s.generateName("gw-");
    const domainName = `${testID}.dns-api.test`;
    const gatewayAddress = "k8s-public-123456.ap-northeast-1.elb.amazonaws.com";
    const aliasDNSName = `${gatewayAddress}.`;
    const aliasHostedZoneID = "Z14GRHDCWA56QT";

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

    s.when("an application Zone is created");
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
        allowedRecordSets: [
          {
            namespaces: {
              selector: {
                matchLabels: {
                  "dns-api": "system",
                },
              },
            },
            records: [
              {
                name: {
                  pattern: "api",
                },
                types: ["A", "AAAA"],
              },
            ],
          },
        ],
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

    s.then("the Route 53 hosted zone exists before Gateway DNS is generated");
    await s.exec(
      {
        do: async () => {
          await assertHostedZoneExists(hostedZoneID, domainName);
        },
      },
      waitForRoute53,
    );

    s.when("a Gateway listener has a concrete hostname and a Route wildcard matches it");
    await platform.apply<Gateway>({
      apiVersion: "gateway.networking.k8s.io/v1",
      kind: "Gateway",
      metadata: { name: "public" },
      spec: {
        gatewayClassName: "example",
        listeners: [
          {
            name: "https-api",
            protocol: "HTTPS",
            port: 443,
            hostname: `api.${domainName}`,
            allowedRoutes: {
              namespaces: {
                from: "All",
              },
            },
          },
        ],
      },
    });
    await app.apply<HTTPRoute>({
      apiVersion: "gateway.networking.k8s.io/v1",
      kind: "HTTPRoute",
      metadata: { name: "web" },
      spec: {
        parentRefs: [
          {
            namespace: platform.name,
            name: "public",
            sectionName: "https-api",
          },
        ],
        hostnames: [`*.${domainName}`],
        rules: [
          {
            backendRefs: [
              {
                name: "web",
                port: 8080,
              },
            ],
          },
        ],
      },
    });
    await s.exec({
      do: async ({ $ }) => {
        await $`kubectl -n ${platform.name} patch gateway public --subresource=status --type=merge -p ${JSON.stringify({
          status: {
            addresses: [
              {
                type: "Hostname",
                value: gatewayAddress,
              },
            ],
          },
        })}`;
        await $`kubectl -n ${app.name} patch httproute web --subresource=status --type=merge -p ${JSON.stringify({
          status: {
            parents: [
              {
                parentRef: {
                  namespace: platform.name,
                  name: "public",
                  sectionName: "https-api",
                },
                controllerName: "example.com/gateway-controller",
                conditions: [
                  {
                    type: "Accepted",
                    status: "True",
                    reason: "Accepted",
                    message: "Accepted by test Gateway controller status.",
                    lastTransitionTime: new Date().toISOString(),
                  },
                ],
              },
            ],
          },
        })}`;
      },
    });

    s.then("EndpointRecordSet explains that the concrete hostname was selected");
    await s.exec(
      {
        do: async ({ $ }) => {
          const selector = [
            "app.kubernetes.io/managed-by=dns-api-gateway-endpoint",
            `gateway.endpoint.dns.appthrust.io/gateway-namespace=${app.name}`,
            "gateway.endpoint.dns.appthrust.io/gateway-name=public",
          ].join(",");
          const output =
            await $`kubectl -n dns-api-system get endpointrecordsets.endpoint.dns.appthrust.io -l ${selector} -o json`.text();
          const list = JSON.parse(output) as { items?: Array<EndpointRecordSet> };
          expect(list.items).toHaveLength(1);
          const endpointRecordSet = requireValue(list.items?.[0]);
          expect(endpointRecordSet.status?.conditions).toContainEqual(
            expect.objectContaining({
              type: "Resolved",
              status: "True",
            }),
          );
          expect(endpointRecordSet.status?.hostnameCount).toBe(1);
          const hostname = endpointRecordSet.status?.hostnames?.find(
            (item) => item.hostname === `api.${domainName}`,
          );
          expect(hostname).toBeDefined();
          expect(hostname?.conditions).toContainEqual(
            expect.objectContaining({
              type: "Resolved",
              status: "True",
            }),
          );
          expect(hostname?.zone).toMatchObject({
            ref: {
              namespace: app.name,
              name: "apps-example-com",
            },
            domainName,
          });
          expect(recordSetTypes(hostname?.recordSets)).toEqual(["A", "AAAA"]);
          for (const recordSet of hostname?.recordSets ?? []) {
            expect(recordSet.ref.namespace).toBe("dns-api-system");
            expect(recordSet.name).toBe("api");
            expect(recordSet.fragment?.options?.alias).toMatchObject({
              dnsName: aliasDNSName,
              hostedZoneID: aliasHostedZoneID,
              evaluateTargetHealth: true,
            });
          }
        },
      },
      waitForRoute53,
    );

    s.then("Route 53 has generated ALIAS A and AAAA records for the Gateway hostname");
    await s.exec(
      {
        do: async () => {
          await assertAliasRecordSetExists({
            hostedZoneID,
            recordName: `api.${domainName}.`,
            type: "A",
            aliasDNSName,
            aliasHostedZoneID,
          });
          await assertAliasRecordSetExists({
            hostedZoneID,
            recordName: `api.${domainName}.`,
            type: "AAAA",
            aliasDNSName,
            aliasHostedZoneID,
          });
        },
      },
      waitForRoute53,
    );

    s.when("the HTTPRoute is deleted");
    await app.delete<HTTPRoute>(
      {
        apiVersion: "gateway.networking.k8s.io/v1",
        kind: "HTTPRoute",
        name: "web",
      },
      waitForRoute53,
    );
    await app.assertAbsence<HTTPRoute>(
      {
        apiVersion: "gateway.networking.k8s.io/v1",
        kind: "HTTPRoute",
        name: "web",
      },
      waitForRoute53,
    );

    s.then("the generated Route 53 ALIAS records are gone");
    await s.exec(
      {
        do: async () => {
          await assertRecordSetAbsent(hostedZoneID, `api.${domainName}.`, "A");
          await assertRecordSetAbsent(hostedZoneID, `api.${domainName}.`, "AAAA");
        },
      },
      waitForRoute53,
    );

    s.when("the Zone is deleted");
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

type EndpointRecordSetHostnameStatus = NonNullable<
  NonNullable<EndpointRecordSet["status"]>["hostnames"]
>[number];

function recordSetTypes(recordSets: EndpointRecordSetHostnameStatus["recordSets"]) {
  return (recordSets ?? []).map((recordSet) => recordSet.type).sort();
}

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

async function assertAliasRecordSetExists(input: {
  hostedZoneID: string;
  recordName: string;
  type: "A" | "AAAA";
  aliasDNSName: string;
  aliasHostedZoneID: string;
}) {
  const record = await getRecordSet(input.hostedZoneID, input.recordName, input.type);
  expect(record?.Name).toBe(input.recordName);
  expect(record?.Type).toBe(input.type);
  expect(record?.TTL).toBeUndefined();
  expect(record?.ResourceRecords).toBeUndefined();
  expect(record?.AliasTarget).toMatchObject({
    DNSName: input.aliasDNSName,
    HostedZoneId: input.aliasHostedZoneID,
    EvaluateTargetHealth: true,
  });
}

async function assertRecordSetAbsent(
  hostedZoneID: string,
  recordName: string,
  type: "A" | "AAAA",
) {
  const record = await getRecordSet(hostedZoneID, recordName, type);
  expect(record).toBeUndefined();
}

async function getRecordSet(
  hostedZoneID: string,
  recordName: string,
  type: "A" | "AAAA",
) {
  const output = await route53.send(
    new ListResourceRecordSetsCommand({
      HostedZoneId: hostedZoneID,
      StartRecordName: recordName,
      StartRecordType: type,
      MaxItems: 1,
    }),
  );
  const record = output.ResourceRecordSets?.[0];
  if (record?.Name !== recordName || record.Type !== type) {
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

function requireValue<T>(value: T | undefined | null): T {
  expect(value).toBeDefined();
  return value as T;
}
