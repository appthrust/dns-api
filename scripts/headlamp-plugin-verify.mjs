#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { gunzipSync } from 'node:zlib';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';

const repoRoot = process.cwd();
const packageDir = path.join(repoRoot, 'app/headlamp-plugin');
const packageName = 'headlamp-plugin-dns-api';
const rootLicense = await readFile(path.join(repoRoot, 'LICENSE'));
const metadataPath = path.join(
  repoRoot,
  'deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml'
);
const strictMetadata = process.argv.includes('--strict-metadata');
const version = normalizeVersion(process.env.VERSION || process.env.RELEASE_VERSION || '');
const archivePath = await findArchive(version);
const checksumPath = `${archivePath}.sha256`;

const archive = await readFile(archivePath);
const checksum = createHash('sha256').update(archive).digest('hex');
const checksumFile = await readFile(checksumPath, 'utf8').catch(error => {
  throw new Error(`missing checksum file ${path.relative(repoRoot, checksumPath)}: ${error.message}`);
});

if (!checksumFile.includes(checksum)) {
  throw new Error(`${path.relative(repoRoot, checksumPath)} does not match archive checksum`);
}

const entries = listTarEntries(gunzipSync(archive));
const names = entries.map(entry => entry.name);
const required = [`${packageName}/`, `${packageName}/package.json`, `${packageName}/main.js`];
required.splice(2, 0, `${packageName}/LICENSE`);
for (const name of required) {
  if (!names.includes(name)) {
    throw new Error(`archive is missing ${name}`);
  }
}

const archivedLicense = entries.find(entry => entry.name === `${packageName}/LICENSE`)?.body;
if (!archivedLicense?.equals(rootLicense)) {
  throw new Error('archive LICENSE must match repository root LICENSE');
}

for (const name of names) {
  if (!name.startsWith(`${packageName}/`)) {
    throw new Error(`archive entry must be under ${packageName}/: ${name}`);
  }
  if (
    name.includes('/node_modules/') ||
    name.includes('/src/') ||
    name.endsWith('.map') ||
    name.includes('/.env') ||
    name.includes('/.kube') ||
    name.includes('/test-results/')
  ) {
    throw new Error(`archive contains a forbidden entry: ${name}`);
  }
}

await verifyMetadata({ checksum, strict: strictMetadata });
console.log(`Verified ${path.relative(repoRoot, archivePath)}`);

async function findArchive(requestedVersion) {
  if (requestedVersion) {
    return path.join(packageDir, `${packageName}-v${requestedVersion}.tar.gz`);
  }
  const entries = await readdir(packageDir);
  const archives = entries
    .filter(name => new RegExp(`^${packageName}-v\\d+\\.\\d+\\.\\d+(?:[-+][0-9A-Za-z.-]+)?\\.tar\\.gz$`).test(name))
    .sort()
    .reverse();
  if (archives.length === 0) {
    throw new Error(`no ${packageName} archive found under ${path.relative(repoRoot, packageDir)}`);
  }
  return path.join(packageDir, archives[0]);
}

function normalizeVersion(value) {
  if (!value) {
    return '';
  }
  const normalized = value.startsWith('v') ? value.slice(1) : value;
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(normalized)) {
    throw new Error(`VERSION must be a semantic version, got ${value}`);
  }
  return normalized;
}

function listTarEntries(buffer) {
  const entries = [];
  for (let offset = 0; offset < buffer.length; ) {
    const header = buffer.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) {
      break;
    }
    const name = readNullTerminated(header, 0, 100);
    const size = Number.parseInt(readNullTerminated(header, 124, 12).trim() || '0', 8);
    const bodyStart = offset + 512;
    entries.push({ name, size, body: buffer.subarray(bodyStart, bodyStart + size) });
    offset = bodyStart + Math.ceil(size / 512) * 512;
  }
  return entries;
}

function readNullTerminated(buffer, offset, length) {
  const slice = buffer.subarray(offset, offset + length);
  const end = slice.indexOf(0);
  return slice.subarray(0, end === -1 ? length : end).toString('utf8');
}

async function verifyMetadata({ checksum, strict }) {
  const metadata = await readFile(metadataPath, 'utf8').catch(error => {
    throw new Error(`missing Artifact Hub metadata: ${error.message}`);
  });
  const requiredFields = [
    'version',
    'name',
    'displayName',
    'createdAt',
    'logoURL',
    'description',
    'license',
    'homeURL',
  ];
  for (const field of requiredFields) {
    if (!new RegExp(`^${field}:\\s*.+$`, 'm').test(metadata)) {
      throw new Error(`artifacthub-pkg.yml is missing ${field}`);
    }
  }

  const annotations = readAnnotationMap(metadata);
  const archiveURL = annotations.get('headlamp/plugin/archive-url');
  const archiveChecksum = annotations.get('headlamp/plugin/archive-checksum');
  const versionCompat = annotations.get('headlamp/plugin/version-compat');
  const distroCompat = annotations.get('headlamp/plugin/distro-compat');

  if (!archiveURL?.startsWith('https://github.com/appthrust/dns-api/releases/download/v')) {
    throw new Error('headlamp/plugin/archive-url must point to GitHub Releases');
  }
  if (!archiveChecksum || !/^sha256:[0-9a-f]{64}$|^sha256:<checksum>$/.test(archiveChecksum)) {
    throw new Error('headlamp/plugin/archive-checksum must be sha256:<hex>');
  }
  if (versionCompat !== '>=0.42.0 <0.43.0') {
    throw new Error('headlamp/plugin/version-compat must be >=0.42.0 <0.43.0');
  }
  if (distroCompat !== 'app,mac,linux') {
    throw new Error('headlamp/plugin/distro-compat must be app,mac,linux');
  }
  if (strict && archiveChecksum !== `sha256:${checksum}`) {
    throw new Error('Artifact Hub archive checksum does not match the packaged archive');
  }
}

function readAnnotationMap(metadata) {
  const annotations = new Map();
  const lines = metadata.split(/\r?\n/);
  let inAnnotations = false;
  for (const line of lines) {
    if (line === 'annotations:') {
      inAnnotations = true;
      continue;
    }
    if (!inAnnotations) {
      continue;
    }
    if (line && !line.startsWith('  ')) {
      break;
    }
    const match = line.match(/^  ([^:]+\/[^:]+):\s*(.+)$/);
    if (match) {
      annotations.set(match[1], match[2].replace(/^"|"$/g, ''));
    }
  }
  return annotations;
}
