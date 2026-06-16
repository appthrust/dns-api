export function canonicalRecordName(ownerName: string, domainName: string) {
  if (ownerName === '@') {
    return `${domainName}.`;
  }
  if (ownerName === '*') {
    return `*.${domainName}.`;
  }
  return `${ownerName}.${domainName}.`;
}
