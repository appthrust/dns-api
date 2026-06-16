#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';

const repoRoot = process.cwd();
const packageDir = path.join(repoRoot, 'app/headlamp-plugin');
const distDir = path.join(packageDir, 'dist');
const packageName = 'headlamp-plugin-dns-api';

const packageJSONPath = path.join(packageDir, 'package.json');
const licensePath = path.join(repoRoot, 'LICENSE');
const packageJSON = JSON.parse(await readFile(packageJSONPath, 'utf8'));
const releaseVersion = normalizeVersion(process.env.VERSION || packageJSON.version);
const archiveName = `${packageName}-v${releaseVersion}.tar.gz`;
const archivePath = path.join(packageDir, archiveName);
const checksumPath = `${archivePath}.sha256`;

await ensureFile(path.join(distDir, 'main.js'));
await ensureFile(licensePath);

const entries = [
  directoryEntry(`${packageName}/`),
  await fileEntry(`${packageName}/LICENSE`, licensePath),
  await fileEntry(`${packageName}/package.json`, packageJSONPath),
  await fileEntry(`${packageName}/main.js`, path.join(distDir, 'main.js')),
];

const tarball = Buffer.concat(entries.flatMap(entry => [entry.header, entry.body]));
const archive = gzipSync(tarball, { level: 9, mtime: 0 });
// Node/zlib writes a platform-specific gzip OS byte. Normalize it so the
// release archive checksum is the same on macOS and Linux.
archive[9] = 0xff;
const checksum = createHash('sha256').update(archive).digest('hex');

await rm(archivePath, { force: true });
await rm(checksumPath, { force: true });
await mkdir(packageDir, { recursive: true });
await writeFile(archivePath, archive);
await writeFile(checksumPath, `${checksum}  ${archiveName}\n`);

console.log(`Created ${path.relative(repoRoot, archivePath)}`);
console.log(`Created ${path.relative(repoRoot, checksumPath)}`);
console.log(`sha256:${checksum}`);

function normalizeVersion(value) {
  const version = value.startsWith('v') ? value.slice(1) : value;
  if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`VERSION must be a semantic version, got ${value}`);
  }
  return version;
}

async function ensureFile(filePath) {
  const info = await stat(filePath).catch(() => null);
  if (!info?.isFile()) {
    throw new Error(`missing required file: ${path.relative(repoRoot, filePath)}`);
  }
}

function directoryEntry(name) {
  return {
    header: tarHeader({ name, mode: 0o755, size: 0, type: '5' }),
    body: Buffer.alloc(0),
  };
}

async function fileEntry(name, sourcePath) {
  const body = await readFile(sourcePath);
  return {
    header: tarHeader({ name, mode: 0o644, size: body.length, type: '0' }),
    body: pad512(body),
  };
}

function tarHeader({ name, mode, size, type }) {
  const header = Buffer.alloc(512, 0);
  writeString(header, name, 0, 100);
  writeOctal(header, mode, 100, 8);
  writeOctal(header, 0, 108, 8);
  writeOctal(header, 0, 116, 8);
  writeOctal(header, size, 124, 12);
  writeOctal(header, 0, 136, 12);
  header.fill(0x20, 148, 156);
  writeString(header, type, 156, 1);
  writeString(header, 'ustar', 257, 6);
  writeString(header, '00', 263, 2);
  writeString(header, 'root', 265, 32);
  writeString(header, 'root', 297, 32);

  let checksum = 0;
  for (const byte of header) {
    checksum += byte;
  }
  writeOctal(header, checksum, 148, 8);
  header[155] = 0x20;
  return header;
}

function writeString(buffer, value, offset, length) {
  buffer.write(value.slice(0, length), offset, length, 'utf8');
}

function writeOctal(buffer, value, offset, length) {
  const octal = value.toString(8).padStart(length - 1, '0');
  buffer.write(octal, offset, length - 1, 'ascii');
  buffer[offset + length - 1] = 0;
}

function pad512(buffer) {
  const remainder = buffer.length % 512;
  if (remainder === 0) {
    return buffer;
  }
  return Buffer.concat([buffer, Buffer.alloc(512 - remainder, 0)]);
}
