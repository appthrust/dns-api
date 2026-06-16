import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare AAAA RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "an AAAA RecordSet",
      resourceName: "ipv6",
      initial: {
        spec: baseSpec("AAAA", "ipv6", {
          ttl: 300,
          aaaa: { addresses: ["2001:db8::10"] },
        }),
        expected: [
          {
            type: "AAAA",
            name: recordName("ipv6", "${domainName}"),
            content: "2001:db8::10",
            ttl: 300,
            proxied: false,
            normalizeContent: "ipv6",
          },
        ],
      },
      update: {
        spec: baseSpec("AAAA", "ipv6", {
          ttl: 600,
          aaaa: { addresses: ["2001:db8::20"] },
        }),
        expected: [
          {
            type: "AAAA",
            name: recordName("ipv6", "${domainName}"),
            content: "2001:db8::20",
            ttl: 600,
            proxied: false,
            normalizeContent: "ipv6",
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
