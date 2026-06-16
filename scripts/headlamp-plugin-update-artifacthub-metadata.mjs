#!/usr/bin/env node
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';

const repoRoot = process.cwd();
const packageDir = path.join(repoRoot, 'app/headlamp-plugin');
const packageName = 'headlamp-plugin-dns-api';
const metadataPath = path.join(
  repoRoot,
  'deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml'
);

const packageJSON = JSON.parse(await readFile(path.join(packageDir, 'package.json'), 'utf8'));
const version = normalizeVersion(process.env.VERSION || process.env.RELEASE_VERSION || packageJSON.version);
const archiveName = `${packageName}-v${version}.tar.gz`;
const checksumPath = path.join(packageDir, `${archiveName}.sha256`);
const checksumFile = await readFile(checksumPath, 'utf8').catch(error => {
  throw new Error(`missing checksum file ${path.relative(repoRoot, checksumPath)}: ${error.message}`);
});
const checksum = checksumFile.match(/\b[0-9a-f]{64}\b/)?.[0];
if (!checksum) {
  throw new Error(`${path.relative(repoRoot, checksumPath)} must contain a sha256 checksum`);
}

let metadata = await readFile(metadataPath, 'utf8');
metadata = replaceLine(metadata, 'version', version);
metadata = replaceAnnotation(
  metadata,
  'headlamp/plugin/archive-url',
  `https://github.com/appthrust/dns-api/releases/download/v${version}/${archiveName}`
);
metadata = replaceAnnotation(metadata, 'headlamp/plugin/archive-checksum', `sha256:${checksum}`);

await writeFile(metadataPath, metadata);
console.log(`Updated ${path.relative(repoRoot, metadataPath)}`);
console.log(`sha256:${checksum}`);

function normalizeVersion(value) {
  const version = value.startsWith('v') ? value.slice(1) : value;
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`VERSION must be a semantic version, got ${value}`);
  }
  return version;
}

function replaceLine(content, field, value) {
  const pattern = new RegExp(`^${escapeRegExp(field)}:\\s*.*$`, 'm');
  if (!pattern.test(content)) {
    throw new Error(`metadata is missing ${field}`);
  }
  return content.replace(pattern, `${field}: ${value}`);
}

function replaceAnnotation(content, key, value) {
  const pattern = new RegExp(`^(\\s*${escapeRegExp(key)}:\\s*).*$`, 'm');
  if (!pattern.test(content)) {
    throw new Error(`metadata is missing annotation ${key}`);
  }
  return content.replace(pattern, `$1${value}`);
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
