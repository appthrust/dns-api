// Package v1alpha1 contains the hosted provider's inline class parameters and
// HostedZone serving snapshots. The controller owns snapshot specs; each server
// owns only its status.servers entry, keyed by its namespace/name identity.
// +kubebuilder:object:generate=true
// +groupName=hosted.dns.appthrust.io
package v1alpha1
