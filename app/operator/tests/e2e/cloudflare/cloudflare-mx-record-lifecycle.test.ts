import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare MX RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "an MX RecordSet",
      resourceName: "mail",
      initial: {
        spec: baseSpec("MX", "@", {
          ttl: 300,
          mx: {
            records: [
              { preference: 10, exchange: "mail1.example.net" },
              { preference: 20, exchange: "mail2.example.net" },
            ],
          },
        }),
        expected: [
          {
            type: "MX",
            name: recordName("@", "${domainName}"),
            content: "mail1.example.net",
            ttl: 300,
            priority: 10,
            normalizeContent: "dns-name",
          },
          {
            type: "MX",
            name: recordName("@", "${domainName}"),
            content: "mail2.example.net",
            ttl: 300,
            priority: 20,
            normalizeContent: "dns-name",
          },
        ],
      },
      update: {
        spec: baseSpec("MX", "@", {
          ttl: 300,
          mx: {
            records: [{ preference: 5, exchange: "mail.example.net" }],
          },
        }),
        expected: [
          {
            type: "MX",
            name: recordName("@", "${domainName}"),
            content: "mail.example.net",
            ttl: 300,
            priority: 5,
            normalizeContent: "dns-name",
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
