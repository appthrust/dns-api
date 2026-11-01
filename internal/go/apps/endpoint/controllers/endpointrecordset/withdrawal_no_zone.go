package endpointrecordset

import (
	"context"
	"fmt"
	"strings"

	"github.com/appthrust/dns-api/internal/go/apps/endpoint/retirement"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// unpublishedWithoutZone proves absence of a managed publication obligation, not
// absence of arbitrary customer DNS. Missing authority after publication remains
// blocked. The caller has already received an explicit ZoneNotResolved result.
func (r *Reconciler) unpublishedWithoutZone(ctx context.Context, ers *endpointv1alpha1.EndpointRecordSet, namespace, hostname string) (bool, error) {
	if meta.IsStatusConditionTrue(ers.Status.Conditions, retirement.ProtectedCondition) || ers.Status.GeneratedRecordSetCount != 0 {
		return false, nil
	}
	for _, published := range ers.Status.Hostnames {
		if published.Zone != nil || len(published.RecordSets) != 0 {
			return false, nil
		}
	}
	if r.APIReader == nil {
		return false, fmt.Errorf("%w: authoritative reader is required to prove an unpublished endpoint", retirement.ErrCapturePending)
	}
	// ZoneNotResolved also covers conversion/capability rejection. A matching
	// Zone, even an unusable one, is not evidence that no authority ever existed.
	var zones dnsv1alpha1.ZoneList
	if err := r.APIReader.List(ctx, &zones); err != nil {
		return false, fmt.Errorf("read publication Zones: %w", err)
	}
	for _, zone := range zones.Items {
		if hostname == zone.Spec.DomainName || strings.HasSuffix(hostname, "."+zone.Spec.DomainName) {
			return false, nil
		}
	}
	// A cached empty list can lag a successful Create followed by a failed
	// status write. This live, label-bound read closes that crash/cache window.
	// Even a deleting child or foreign manager on the same source holds capture.
	var records dnsv1alpha1.RecordSetList
	if err := r.APIReader.List(ctx, &records, client.InNamespace(namespace), client.MatchingLabels{
		generatedLabelEndpointRecordSetNamespace: ers.Namespace,
		generatedLabelEndpointRecordSetName:      ers.Name,
	}, client.Limit(1)); err != nil {
		return false, fmt.Errorf("read generated RecordSets for unpublished endpoint: %w", err)
	}
	return len(records.Items) == 0 && records.Continue == "" &&
		(records.RemainingItemCount == nil || *records.RemainingItemCount == 0), nil
}
