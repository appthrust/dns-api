import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare proxied A RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "a proxied A RecordSet",
      resourceName: "proxy",
      allowProviderInvalidRequest: true,
      initial: {
        spec: baseSpec("A", "proxy", {
          a: { addresses: ["192.0.2.30"] },
          options: {
            ttl: "Auto",
            proxied: true,
            comment: "proxied app endpoint",
            tags: ["app:frontend", "owner:platform"],
          },
        }),
        expected: [
          {
            type: "A",
            name: recordName("proxy", "${domainName}"),
            content: "192.0.2.30",
            ttl: 1,
            proxied: true,
            comment: "proxied app endpoint",
            tags: ["app:frontend", "owner:platform"],
            optionalMetadata: true,
          },
        ],
      },
      update: {
        spec: baseSpec("A", "proxy", {
          a: { addresses: ["192.0.2.30"] },
          options: {
            ttl: "Auto",
            proxied: true,
            comment: "proxied app endpoint updated",
            tags: ["app:frontend", "owner:sre"],
          },
        }),
        expected: [
          {
            type: "A",
            name: recordName("proxy", "${domainName}"),
            content: "192.0.2.30",
            ttl: 1,
            proxied: true,
            comment: "proxied app endpoint updated",
            tags: ["app:frontend", "owner:sre"],
            optionalMetadata: true,
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
