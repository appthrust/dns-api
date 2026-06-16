import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare CAA RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "a CAA RecordSet",
      resourceName: "ca-policy",
      initial: {
        spec: baseSpec("CAA", "@", {
          ttl: 300,
          caa: {
            records: [
              { flags: 0, tag: "issue", value: "letsencrypt.org" },
              { flags: 0, tag: "iodef", value: "mailto:security@example.com" },
            ],
          },
        }),
        expected: [
          {
            type: "CAA",
            name: recordName("@", "${domainName}"),
            content: "letsencrypt.org",
            ttl: 300,
            caa: { flags: 0, tag: "issue", value: "letsencrypt.org" },
          },
          {
            type: "CAA",
            name: recordName("@", "${domainName}"),
            content: "mailto:security@example.com",
            ttl: 300,
            caa: { flags: 0, tag: "iodef", value: "mailto:security@example.com" },
          },
        ],
      },
      update: {
        spec: baseSpec("CAA", "@", {
          ttl: 300,
          caa: {
            records: [{ flags: 0, tag: "issue", value: "pki.goog" }],
          },
        }),
        expected: [
          {
            type: "CAA",
            name: recordName("@", "${domainName}"),
            content: "pki.goog",
            ttl: 300,
            caa: { flags: 0, tag: "issue", value: "pki.goog" },
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
