import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare A RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "an A RecordSet",
      resourceName: "www",
      initial: {
        spec: baseSpec("A", "www", {
          ttl: 300,
          a: { addresses: ["192.0.2.10", "192.0.2.11"] },
        }),
        expected: [
          {
            type: "A",
            name: recordName("www", "${domainName}"),
            content: "192.0.2.10",
            ttl: 300,
            proxied: false,
          },
          {
            type: "A",
            name: recordName("www", "${domainName}"),
            content: "192.0.2.11",
            ttl: 300,
            proxied: false,
          },
        ],
      },
      update: {
        spec: baseSpec("A", "www", {
          ttl: 600,
          a: { addresses: ["192.0.2.12"] },
        }),
        expected: [
          {
            type: "A",
            name: recordName("www", "${domainName}"),
            content: "192.0.2.12",
            ttl: 600,
            proxied: false,
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
