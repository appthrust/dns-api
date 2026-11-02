package route53

import (
	"context"
	"errors"
	"strconv"
	"strings"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SOA is provider-owned zone configuration, not a supported RecordSet claim type.
const route53RecordTypeSOA dnsv1alpha1.RecordType = "SOA"

type soaRecord struct {
	fields  []string
	minimum int32
}

func parseSOA(value string) (soaRecord, error) {
	fields := strings.Fields(value)
	if len(fields) != 7 {
		return soaRecord{}, errors.New("Route 53 hosted zone apex SOA must contain exactly seven fields")
	}
	minimum, err := strconv.ParseInt(fields[6], 10, 32)
	if err != nil || minimum < 0 {
		return soaRecord{}, errors.New("Route 53 hosted zone apex SOA MINIMUM must be between 0 and 2147483647")
	}
	return soaRecord{fields: fields, minimum: int32(minimum)}, nil
}

func (soa soaRecord) withMinimum(minimum int32) string {
	soa.fields[6] = strconv.FormatInt(int64(minimum), 10)
	return strings.Join(soa.fields, " ")
}

func (r *ZoneReconciler) reconcileNegativeCacheTTL(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, hostedZone HostedZone, params *route53v1alpha1.Route53ZoneClassParameters) (ctrl.Result, bool, error) {
	if params.NegativeCacheTTLSeconds == nil {
		return ctrl.Result{}, false, nil
	}
	record, err := provider.GetRecordSet(ctx, hostedZone.ID, hostedZone.Name, route53RecordTypeSOA)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if record == nil || record.TTL == nil || len(record.Values) != 1 {
		return ctrl.Result{}, true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ExternalResourceMismatch", "Route 53 hosted zone apex SOA must contain exactly one value and a TTL")
	}
	soa, err := parseSOA(record.Values[0])
	if err != nil {
		return ctrl.Result{}, true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ExternalResourceMismatch", err.Error())
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.NegativeCacheTTLSeconds = soa.minimum
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	if soa.minimum == *params.NegativeCacheTTLSeconds {
		return ctrl.Result{}, false, nil
	}

	// Preserve the RRset TTL and all six other SOA fields, including SERIAL.
	record.Values[0] = soa.withMinimum(*params.NegativeCacheTTLSeconds)
	change, err := provider.ChangeRecordSets(ctx, hostedZone.ID, []RecordSetChange{{Action: RecordSetChangeActionUpsert, RecordSet: *record}})
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingHostedZoneChange = pendingChangeFromChange(change, "NegativeCacheTTL")
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	// Use the existing hosted-zone change barrier. Publish only observed MINIMUM,
	// and re-read the SOA after INSYNC rather than reporting the submitted value.
	return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone change is pending")
}
