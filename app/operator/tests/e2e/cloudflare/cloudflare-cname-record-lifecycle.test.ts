import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare CNAME RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "a CNAME RecordSet",
      resourceName: "www",
      initial: {
        spec: baseSpec("CNAME", "www", {
          ttl: 300,
          cname: { target: "target.example.net" },
        }),
        expected: [
          {
            type: "CNAME",
            name: recordName("www", "${domainName}"),
            content: "target.example.net",
            ttl: 300,
            proxied: false,
            normalizeContent: "dns-name",
          },
        ],
      },
      update: {
        spec: baseSpec("CNAME", "www", {
          ttl: 300,
          cname: { target: "target2.example.net" },
        }),
        expected: [
          {
            type: "CNAME",
            name: recordName("www", "${domainName}"),
            content: "target2.example.net",
            ttl: 300,
            proxied: false,
            normalizeContent: "dns-name",
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
