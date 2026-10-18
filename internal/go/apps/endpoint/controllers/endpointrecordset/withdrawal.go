package endpointrecordset

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	withdrawalCreatorAnnotation = "withdrawal.dns.appthrust.io/source-kind"
	withdrawalSourceNamespaceAnnotation = "withdrawal.dns.appthrust.io/source-namespace"
	withdrawalSourceNameAnnotation = "withdrawal.dns.appthrust.io/source-name"
	withdrawalSourceUIDAnnotation = "withdrawal.dns.appthrust.io/source-uid"
	withdrawalSourceGenerationAnnotation = "withdrawal.dns.appthrust.io/source-generation"
	withdrawalSourceHostnameAnnotation = "withdrawal.dns.appthrust.io/source-hostname"
)

func hasRetirements(ers *endpointv1alpha1.EndpointRecordSet) bool {
	return len(ers.Spec.RetiredTargets) != 0 || len(ers.Spec.RetiredHostnames) != 0
}

func retireOnly(ers *endpointv1alpha1.EndpointRecordSet) bool {
	return len(ers.Spec.Hostnames) == 0 || len(ers.Spec.Targets) == 0
}

// captureWithdrawals performs create-before-prune. Successful Create returns
// the persisted object; AlreadyExists is followed by a shape-checked Get. A
// stale cache miss fails closed and retries without discarding source intent.
func (r *Reconciler) captureWithdrawals(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, namespace string, includeLive bool) error {
	if len(ers.Spec.RetiredTargets) > retirement.MaxRetiredTargets || len(ers.Spec.RetiredHostnames) > retirement.MaxRetiredHostnames {
		return fmt.Errorf("%w: retirement collection limits exceeded", retirement.ErrCapturePending)
	}
	for _, entry := range ers.Spec.RetiredTargets {
		if len(entry.Hostnames) > retirement.MaxRetiredHostnames {
			return fmt.Errorf("%w: target hostname snapshot limit exceeded", retirement.ErrCapturePending)
		}
	}
	for _, entry := range ers.Spec.RetiredHostnames {
		if len(entry.Addresses) > retirement.MaxRetiredTargets {
			return fmt.Errorf("%w: hostname address snapshot limit exceeded", retirement.ErrCapturePending)
		}
	}
	type binding struct {
		zone *dnsv1alpha1.Zone
		protected bool
	}
	bindings := map[string]binding{}
	captured := map[string]struct{}{}
	capture := func(hostname, value string, generation int64) error {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" {
			return fmt.Errorf("%w: invalid retired address %q", retirement.ErrCapturePending, value)
		}
		address = address.Unmap()
		hostname = retirement.CanonicalHostname(hostname)
		key := hostname + "/" + address.String()
		selected, found := bindings[key]
		if !found {
			zone, err := r.withdrawalZone(ctx, ers, namespace, hostname, address)
			if err != nil {
				return err
			}
			supported, err := r.withdrawalSupported(ctx, zone.Spec.Provider)
			if err != nil {
				return err
			}
			selected = binding{zone: zone, protected: supported}
			bindings[key] = selected
		}
		if !selected.protected {
			return nil
		}
		if ers.UID == "" || generation < 1 {
			return fmt.Errorf("%w: EndpointRecordSet UID and retirement generation are required", retirement.ErrCapturePending)
		}
		claim := withdrawalForPair(ers, namespace, selected.zone, hostname, address, generation)
		if _, found := captured[claim.Name]; found {
			return nil
		}
		if err := r.ensureWithdrawal(ctx, claim); err != nil {
			return err
		}
		captured[claim.Name] = struct{}{}
		return nil
	}
	for _, entry := range ers.Spec.RetiredTargets {
		for _, hostname := range entry.Hostnames {
			if err := capture(hostname, entry.Address, entry.SourceGeneration); err != nil {
				return err
			}
		}
	}
	for _, entry := range ers.Spec.RetiredHostnames {
		for _, address := range entry.Addresses {
			if err := capture(entry.Hostname, address, entry.SourceGeneration); err != nil {
				return err
			}
		}
	}
	if includeLive && retirement.Protected(ers) {
		for _, target := range ers.Spec.Targets {
			if target.Type != endpointv1alpha1.EndpointTargetTypeIPAddress {
				continue
			}
			for _, hostname := range ers.Spec.Hostnames {
				if err := capture(hostname, target.Value, ers.Generation); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// withdrawalZone prefers the retained publication binding. Without one, run the
// same publication resolver with a nonempty single-pair snapshot, including
// conversion rejection and Zone fallback. This never applies its RecordSets.
func (r *Reconciler) withdrawalZone(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, namespace, hostname string, address netip.Addr) (*dnsv1alpha1.Zone, error) {
	for _, published := range ers.Status.Hostnames {
		if retirement.CanonicalHostname(published.Hostname) != hostname || published.Zone == nil {
			continue
		}
		var zone dnsv1alpha1.Zone
		if err := r.Get(ctx, client.ObjectKey{Namespace: published.Zone.Ref.Namespace, Name: published.Zone.Ref.Name}, &zone); err != nil {
			return nil, err
		}
		if zone.Spec.DomainName != published.Zone.DomainName || zone.Spec.Provider != published.Zone.Provider {
			return nil, fmt.Errorf("%w: published Zone binding changed for %s", retirement.ErrCapturePending, hostname)
		}
		return &zone, nil
	}
	snapshot := *ers
	snapshot.Spec.Targets = []endpointv1alpha1.EndpointTarget{{Type: endpointv1alpha1.EndpointTargetTypeIPAddress, Value: address.String()}}
	resolved, _, err := r.recordSetsForHostname(ctx, &snapshot, hostname, namespace)
	if err != nil {
		return nil, err
	}
	if resolved.Zone == nil {
		return nil, fmt.Errorf("%w: no publication Zone binding resolved for %s", retirement.ErrCapturePending, hostname)
	}
	return &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: resolved.Zone.Ref.Namespace, Name: resolved.Zone.Ref.Name},
		Spec: dnsv1alpha1.ZoneSpec{DomainName: resolved.Zone.DomainName, Provider: resolved.Zone.Provider},
	}, nil
}

func (r *Reconciler) withdrawalSupported(ctx context.Context, ref dnsv1alpha1.ProviderReference) (bool, error) {
	var provider dnsv1alpha1.Provider
	if err := r.Get(ctx, client.ObjectKey{Name: ref.Name}, &provider); err != nil {
		return false, err
	}
	for _, version := range provider.Spec.Versions {
		if version.Name == ref.Version {
			return version.RecordWithdrawal != nil && version.RecordWithdrawal.Supported, nil
		}
	}
	return false, fmt.Errorf("Provider %s does not declare version %s", ref.Name, ref.Version)
}

func withdrawalForPair(ers *endpointv1alpha1.EndpointRecordSet, namespace string, zone *dnsv1alpha1.Zone, hostname string, address netip.Addr, generation int64) *dnsv1alpha1.RecordWithdrawal {
	owner := relativeRecordName(hostname, zone.Spec.DomainName)
	generationString := strconv.FormatInt(generation, 10)
	sum := sha256.Sum256([]byte(string(ers.UID) + "\x00" + generationString + "\x00" + hostname + "\x00" + address.String()))
	return &dnsv1alpha1.RecordWithdrawal{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: fmt.Sprintf("ers-%x", sum[:])[:63],
			Labels: map[string]string{dnsv1alpha1.AddressLabelKey: dnsv1alpha1.AddressLabelValue(address)},
			Annotations: map[string]string{
				withdrawalCreatorAnnotation: "EndpointRecordSet",
				withdrawalSourceNamespaceAnnotation: ers.Namespace,
				withdrawalSourceNameAnnotation: ers.Name,
				withdrawalSourceUIDAnnotation: string(ers.UID),
				withdrawalSourceGenerationAnnotation: generationString,
				withdrawalSourceHostnameAnnotation: hostname,
			},
		},
		Spec: dnsv1alpha1.RecordWithdrawalSpec{ZoneRef: dnsv1alpha1.ZoneReference{Namespace: &zone.Namespace, Name: zone.Name}, Name: owner, Address: address.String()},
	}
}

func (r *Reconciler) ensureWithdrawal(ctx context.Context, desired *dnsv1alpha1.RecordWithdrawal) error {
	if err := r.Create(ctx, desired, client.FieldOwner(fieldOwner)); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		var current dnsv1alpha1.RecordWithdrawal
		if err := r.Get(ctx, client.ObjectKeyFromObject(desired), &current); err != nil {
			return err
		}
		if !current.DeletionTimestamp.IsZero() || len(current.OwnerReferences) != 0 || len(current.Finalizers) != 0 ||
			!apiequality.Semantic.DeepEqual(current.Spec, desired.Spec) || current.Labels[dnsv1alpha1.AddressLabelKey] != desired.Labels[dnsv1alpha1.AddressLabelKey] {
			return fmt.Errorf("RecordWithdrawal collision at %s/%s: immutable shape or lifecycle differs", desired.Namespace, desired.Name)
		}
		for key, value := range desired.Annotations {
			if current.Annotations[key] != value {
				return fmt.Errorf("RecordWithdrawal collision at %s/%s: source provenance differs", desired.Namespace, desired.Name)
			}
		}
	}
	return nil
}

func (r *Reconciler) reconcileRetirements(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, namespace string) error {
	if err := r.captureWithdrawals(ctx, ers, namespace, false); err != nil {
		return r.captureFailed(ctx, ers, err)
	}
	if !retireOnly(ers) {
		base := ers.DeepCopy()
		ers.Spec.RetiredTargets = nil
		ers.Spec.RetiredHostnames = nil
		return r.Patch(ctx, ers, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	}
	// Keep captured entries until DELETE: admission requires live OR retired
	// intent. Acknowledgment is only capture, never verified DNS withdrawal.
	if _, err := r.deleteGeneratedRecordSets(ctx, namespace, ers.Namespace, ers.Name); err != nil {
		return err
	}
	status := *ers.Status.DeepCopy()
	status.ObservedGeneration = ers.Generation
	setAggregateCondition(&status, retirement.CapturedCondition, metav1.ConditionTrue, ers.Generation, "Captured", "Every retired hostname/address pair has been durably captured.")
	for _, conditionType := range []string{"Resolved", "Accepted", "Programmed", "Ready"} {
		setAggregateCondition(&status, conditionType, metav1.ConditionFalse, ers.Generation, "Retiring", "EndpointRecordSet is retiring; no active records are published.")
	}
	return r.patchStatus(ctx, ers, status)
}

func (r *Reconciler) captureFailed(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, cause error) error {
	status := *ers.Status.DeepCopy()
	message := cause.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	setAggregateCondition(&status, retirement.CapturedCondition, metav1.ConditionFalse, ers.Generation, retirement.PendingCondition, message)
	setAggregateCondition(&status, "Ready", metav1.ConditionFalse, ers.Generation, retirement.PendingCondition, message)
	if err := r.patchStatus(ctx, ers, status); err != nil {
		return err
	}
	return cause
}

// annotateWithdrawalStatus publishes the marker consumed by provider-neutral
// sources. True is sticky for an ERS incarnation. Unknown never permits loss.
func (r *Reconciler) annotateWithdrawalStatus(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, status *endpointv1alpha1.EndpointRecordSetStatus) {
	protection := metav1.ConditionFalse
	if meta.IsStatusConditionTrue(ers.Status.Conditions, retirement.ProtectedCondition) {
		protection = metav1.ConditionTrue
	} else {
		if len(status.Hostnames) != len(ers.Spec.Hostnames) || len(status.Hostnames) == 0 {
			protection = metav1.ConditionUnknown
		}
		for _, hostname := range status.Hostnames {
			if hostname.Zone == nil {
				protection = metav1.ConditionUnknown
				continue
			}
			supported, err := r.withdrawalSupported(ctx, hostname.Zone.Provider)
			if err != nil {
				protection = metav1.ConditionUnknown
				continue
			}
			if supported {
				protection = metav1.ConditionTrue
				break
			}
		}
	}
	setAggregateCondition(status, retirement.ProtectedCondition, protection, ers.Generation, "ProviderCapability", "Withdrawal protection follows the resolved Provider recordWithdrawal capability; unknown bindings retain intent.")
	setAggregateCondition(status, retirement.CapturedCondition, metav1.ConditionTrue, ers.Generation, "Captured", "No uncaptured retirement entries remain.")
	if pending := meta.FindStatusCondition(ers.Status.Conditions, retirement.PendingCondition); pending != nil {
		meta.SetStatusCondition(&status.Conditions, *pending)
		if pending.Status == metav1.ConditionTrue {
			setAggregateCondition(status, "Ready", metav1.ConditionFalse, ers.Generation, retirement.PendingCondition, pending.Message)
		}
	}
}

func protectionNeedsBarrier(ers *endpointv1alpha1.EndpointRecordSet, status endpointv1alpha1.EndpointRecordSetStatus) bool {
	old := meta.FindStatusCondition(ers.Status.Conditions, retirement.ProtectedCondition)
	return old != nil && old.Status == metav1.ConditionFalse && old.ObservedGeneration == ers.Generation &&
		!slices.ContainsFunc(status.Conditions, func(condition metav1.Condition) bool {
			return condition.Type == retirement.ProtectedCondition && condition.Status == metav1.ConditionFalse
		})
}
