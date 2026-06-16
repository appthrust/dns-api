import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare delegated NS RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "a delegated NS RecordSet",
      resourceName: "delegated",
      initial: {
        spec: baseSpec("NS", "delegated", {
          ttl: 300,
          ns: {
            nameServers: ["ns-111.example-dns.net", "ns-222.example-dns.net"],
          },
        }),
        expected: [
          {
            type: "NS",
            name: recordName("delegated", "${domainName}"),
            content: "ns-111.example-dns.net",
            ttl: 300,
            normalizeContent: "dns-name",
          },
          {
            type: "NS",
            name: recordName("delegated", "${domainName}"),
            content: "ns-222.example-dns.net",
            ttl: 300,
            normalizeContent: "dns-name",
          },
        ],
      },
      update: {
        spec: baseSpec("NS", "delegated", {
          ttl: 300,
          ns: {
            nameServers: ["ns-333.example-dns.net"],
          },
        }),
        expected: [
          {
            type: "NS",
            name: recordName("delegated", "${domainName}"),
            content: "ns-333.example-dns.net",
            ttl: 300,
            normalizeContent: "dns-name",
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
