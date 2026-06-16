import { expect, test } from "bun:test";
import {
  cloudflareTestDomainName,
  cloudflareTestPrefix,
  generateCloudflareTestDomainName,
} from "./cloudflare-test-names";

test("cloudflareTestDomainName uses a dated root-domain shape", () => {
  const name = cloudflareTestDomainName("zone-abC_123", new Date("2026-06-05T05:20:00Z"));

  expect(name).toBe(`${cloudflareTestPrefix}20260605-0520-zoneabc123.com`);
});

test("generateCloudflareTestDomainName regenerates on name collision", async () => {
  const observed: string[] = [];
  let firstCandidate = true;

  const name = await generateCloudflareTestDomainName("zone-collides", async (candidate) => {
    observed.push(candidate);
    if (firstCandidate) {
      firstCandidate = false;
      return true;
    }
    return false;
  });

  expect(observed).toHaveLength(2);
  expect(name).toStartWith(`${cloudflareTestPrefix}`);
  expect(name).toEndWith(".com");
  expect(name).not.toBe(observed[0]);
});
