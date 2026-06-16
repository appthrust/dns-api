import { test } from "@appthrust/kest";
import {
  baseSpec,
  recordName,
  runCloudflareRecordSetLifecycle,
} from "./cloudflare-recordset-helpers";

test(
  "a Cloudflare TXT RecordSet follows the Kubernetes RecordSet lifecycle",
  async (s) => {
    await runCloudflareRecordSetLifecycle(s, {
      title: "a TXT RecordSet",
      resourceName: "acme-challenge",
      initial: {
        spec: baseSpec("TXT", "_acme-challenge", {
          ttl: 300,
          txt: {
            values: ["challenge-token", "v=spf1 include:_spf.example.net ~all"],
          },
        }),
        expected: [
          {
            type: "TXT",
            name: recordName("_acme-challenge", "${domainName}"),
            content: "challenge-token",
            ttl: 300,
            normalizeContent: "txt",
          },
          {
            type: "TXT",
            name: recordName("_acme-challenge", "${domainName}"),
            content: "v=spf1 include:_spf.example.net ~all",
            ttl: 300,
            normalizeContent: "txt",
          },
        ],
      },
      update: {
        spec: baseSpec("TXT", "_acme-challenge", {
          ttl: 300,
          txt: {
            values: ["challenge-token-2"],
          },
        }),
        expected: [
          {
            type: "TXT",
            name: recordName("_acme-challenge", "${domainName}"),
            content: "challenge-token-2",
            ttl: 300,
            normalizeContent: "txt",
          },
        ],
      },
    });
  },
  { timeout: "20m" },
);
