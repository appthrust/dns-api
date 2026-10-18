// Package retirement implements the provider-neutral source side of DEP-0003.
package retirement

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ProtectedCondition = "WithdrawalProtected"
	CapturedCondition = "WithdrawalCaptured"
	PendingCondition = "WithdrawalCapturePending"
	MaxRetiredTargets = 64
	MaxRetiredHostnames = 256
)

var ErrCapturePending = errors.New("WithdrawalCapturePending")

// Protected defaults closed until the endpoint controller has resolved every
// hostname to a Provider without withdrawal support at this exact generation.
func Protected(ers *endpointv1alpha1.EndpointRecordSet) bool {
	condition := meta.FindStatusCondition(ers.Status.Conditions, ProtectedCondition)
	return condition == nil || condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != ers.Generation
}

func Captured(ers *endpointv1alpha1.EndpointRecordSet) bool {
	condition := meta.FindStatusCondition(ers.Status.Conditions, CapturedCondition)
	return condition != nil && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == ers.Generation
}

// Append keeps every existing retirement entry and snapshots old associations.
// It does not mutate current or desired, including on overflow. Generation is
// the pre-update ERS generation, so repeated writes and duplicate collections
// identify the same retirement while a later re-add/remove identifies a new one.
func Append(current *endpointv1alpha1.EndpointRecordSet, desired endpointv1alpha1.EndpointRecordSetSpec) (endpointv1alpha1.EndpointRecordSetSpec, error) {
	out := desired
	out.RetiredTargets = slices.Clone(current.Spec.RetiredTargets)
	out.RetiredHostnames = slices.Clone(current.Spec.RetiredHostnames)
	if !Protected(current) {
		return out, nil
	}
	oldAddresses, err := addresses(current.Spec.Targets)
	if err != nil {
		return out, err
	}
	newAddresses, err := addresses(desired.Targets)
	if err != nil {
		return out, err
	}
	if len(oldAddresses) == 0 || len(current.Spec.Hostnames) == 0 {
		return out, nil
	}
	removedAddresses := make([]string, 0)
	for _, address := range oldAddresses {
		if !slices.Contains(newAddresses, address) || len(desired.Hostnames) == 0 {
			removedAddresses = append(removedAddresses, address)
		}
	}
	removedHostnames := make([]string, 0)
	for _, hostname := range current.Spec.Hostnames {
		if !slices.Contains(desired.Hostnames, hostname) || len(newAddresses) == 0 {
			removedHostnames = append(removedHostnames, hostname)
		}
	}
	if len(removedAddresses) == 0 && len(removedHostnames) == 0 {
		return out, nil
	}
	if current.UID == "" || current.Generation < 1 {
		return out, fmt.Errorf("%w: EndpointRecordSet UID and generation are required", ErrCapturePending)
	}
	if len(current.Spec.Hostnames) > MaxRetiredHostnames || len(oldAddresses) > MaxRetiredTargets ||
		len(out.RetiredTargets)+len(removedAddresses) > MaxRetiredTargets ||
		len(out.RetiredHostnames)+len(removedHostnames) > MaxRetiredHostnames {
		return out, fmt.Errorf("%w: retirement snapshot limits exceeded; old intent retained", ErrCapturePending)
	}
	for _, address := range removedAddresses {
		out.RetiredTargets = append(out.RetiredTargets, endpointv1alpha1.RetiredTarget{
			ID: entryID(current, "target", address), SourceGeneration: current.Generation,
			Address: address, Hostnames: slices.Clone(current.Spec.Hostnames),
		})
	}
	for _, hostname := range removedHostnames {
		out.RetiredHostnames = append(out.RetiredHostnames, endpointv1alpha1.RetiredHostname{
			ID: entryID(current, "hostname", hostname), SourceGeneration: current.Generation,
			Hostname: hostname, Addresses: slices.Clone(oldAddresses),
		})
	}
	return out, nil
}

func entryID(ers *endpointv1alpha1.EndpointRecordSet, kind, value string) string {
	sum := sha256.Sum256([]byte(string(ers.UID) + "\x00" + strconv.FormatInt(ers.Generation, 10) + "\x00" + kind + "\x00" + value))
	return fmt.Sprintf("%x", sum[:])
}

func addresses(targets []endpointv1alpha1.EndpointTarget) ([]string, error) {
	var out []string
	for _, target := range targets {
		if target.Type != endpointv1alpha1.EndpointTargetTypeIPAddress {
			continue
		}
		address, err := netip.ParseAddr(target.Value)
		if err != nil || address.Zone() != "" {
			return nil, fmt.Errorf("%w: invalid IP target %q", ErrCapturePending, target.Value)
		}
		value := address.Unmap().String()
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out, nil
}

// ReportPending leaves the old spec untouched and makes overflow visible. The
// source owns only PendingCondition; Ready is also projected by endpoint.
func ReportPending(ctx context.Context, c client.Client, ers *endpointv1alpha1.EndpointRecordSet, cause error) error {
	base := ers.DeepCopy()
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	for _, conditionType := range []string{PendingCondition, "Ready"} {
		status := metav1.ConditionTrue
		if conditionType == "Ready" {
			status = metav1.ConditionFalse
		}
		meta.SetStatusCondition(&ers.Status.Conditions, metav1.Condition{Type: conditionType, Status: status, ObservedGeneration: ers.Generation, Reason: PendingCondition, Message: message})
	}
	if err := c.Status().Patch(ctx, ers, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return cause
}

// ClearPending is called after a source can represent its complete desired
// transition. It never acknowledges capture or prunes retirement intent.
func ClearPending(ctx context.Context, c client.Client, ers *endpointv1alpha1.EndpointRecordSet) error {
	if !meta.IsStatusConditionTrue(ers.Status.Conditions, PendingCondition) {
		return nil
	}
	base := ers.DeepCopy()
	meta.RemoveStatusCondition(&ers.Status.Conditions, PendingCondition)
	return c.Status().Patch(ctx, ers, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// RetireOrDelete is shared by every source cleanup path. A protected ERS is
// deleted only after a current-generation endpoint capture acknowledgment.
func RetireOrDelete(ctx context.Context, c client.Client, ers *endpointv1alpha1.EndpointRecordSet) error {
	if !ers.DeletionTimestamp.IsZero() {
		return nil
	}
	if !Protected(ers) || (len(ers.Spec.Hostnames) == 0 || len(ers.Spec.Targets) == 0) && Captured(ers) {
		return deleteExact(ctx, c, ers)
	}
	spec, err := Append(ers, endpointv1alpha1.EndpointRecordSetSpec{})
	if err != nil {
		return ReportPending(ctx, c, ers, err)
	}
	if len(spec.RetiredTargets) == 0 && len(spec.RetiredHostnames) == 0 {
		// Hostname targets do not carry address withdrawal obligations.
		return deleteExact(ctx, c, ers)
	}
	if len(ers.Spec.Hostnames) != 0 || len(ers.Spec.Targets) != 0 {
		base := ers.DeepCopy()
		ers.Spec = spec
		if err := c.Patch(ctx, ers, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: waiting for EndpointRecordSet %s/%s capture", ErrCapturePending, ers.Namespace, ers.Name)
}

func deleteExact(ctx context.Context, c client.Client, ers *endpointv1alpha1.EndpointRecordSet) error {
	return client.IgnoreNotFound(c.Delete(ctx, ers, client.Preconditions{UID: &ers.UID, ResourceVersion: &ers.ResourceVersion}))
}

// CanonicalHostname is shared by claim identity and source snapshot consumers.
func CanonicalHostname(hostname string) string {
	return strings.ToLower(strings.TrimSuffix(hostname, "."))
}
