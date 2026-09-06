package route53

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ZoneReconciler) zoneAllowedByClass(ctx context.Context, zoneClass *dnsv1alpha1.ZoneClass, zoneNamespace string) (bool, error) {
	policy := zoneClass.Spec.AllowedZones.Namespaces
	switch namespacePolicyFrom(policy) {
	case dnsv1alpha1.NamespacesFromSame:
		return zoneClass.Namespace == zoneNamespace, nil
	case dnsv1alpha1.NamespacesFromAll:
		return true, nil
	case dnsv1alpha1.NamespacesFromSelector:
		if policy.Selector == nil {
			return false, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(policy.Selector)
		if err != nil {
			return false, err
		}
		var namespace corev1.Namespace
		if err := r.Get(ctx, client.ObjectKey{Name: zoneNamespace}, &namespace); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return selector.Matches(labels.Set(namespace.Labels)), nil
	default:
		return false, nil
	}
}

func (r *ZoneReconciler) setAccepted(ctx context.Context, zone *dnsv1alpha1.Zone, status metav1.ConditionStatus, reason, message string) error {
	return r.patchZoneStatus(ctx, zone, func(zoneStatus *dnsv1alpha1.ZoneStatus) {
		setCondition(&zoneStatus.Conditions, string(dnsv1alpha1.ConditionAccepted), status, reason, message, zone.Generation)
	})
}

func (r *ZoneReconciler) setProgrammed(ctx context.Context, zone *dnsv1alpha1.Zone, status metav1.ConditionStatus, reason, message string) error {
	return r.patchZoneStatus(ctx, zone, func(zoneStatus *dnsv1alpha1.ZoneStatus) {
		setCondition(&zoneStatus.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, zone.Generation)
	})
}

func (r *ZoneReconciler) failProgrammedForProviderError(ctx context.Context, zone *dnsv1alpha1.Zone, err error) (ctrl.Result, error) {
	reason, message := providerErrorCondition(err)
	if statusErr := r.setProgrammed(ctx, zone, metav1.ConditionFalse, reason, message); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	return r.resultForProviderError(err), nil
}

func (r *ZoneReconciler) patchZoneStatus(ctx context.Context, zone *dnsv1alpha1.Zone, mutate func(*dnsv1alpha1.ZoneStatus)) error {
	before := zone.DeepCopy()
	mutate(&zone.Status)
	if equality.Semantic.DeepEqual(before.Status, zone.Status) {
		return nil
	}

	var observed dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &observed); err != nil {
		return client.IgnoreNotFound(err)
	}
	var observedZoneConditions []metav1.Condition
	if observed.Status.Zone != nil {
		observedZoneConditions = observed.Status.Zone.Conditions
	}
	unitBase := observed.DeepCopy()
	unit := &observed
	// The informer may advance between the synthetic Zone observation and this
	// read. Diff the workflow's before/after projections, not its stale snapshot
	// against the latest unit: unrelated writes must not replay completed changes.
	projectZoneStatusToZoneUnit(unitBase, &before.Status)
	projectZoneStatusToZoneUnit(unit, &zone.Status)
	unit.Status.ObservedGeneration = unit.Generation
	projectedZoneConditions := unit.Status.Zone.Conditions
	if equality.Semantic.DeepEqual(before.Status.Conditions, zone.Status.Conditions) {
		// This field is omitted from the patch, so aggregate the current
		// conditions that the API server will preserve, not the stale projection.
		unit.Status.Zone.Conditions = observedZoneConditions
	}
	setZoneUnitProgrammedCondition(unit)
	unit.Status.Zone.Conditions = projectedZoneConditions
	if equality.Semantic.DeepEqual(unitBase.Status, unit.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, unit, client.MergeFrom(unitBase)))
}

func projectZoneStatusToZoneUnit(unit *dnsv1alpha1.ZoneUnit, status *dnsv1alpha1.ZoneStatus) {
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
	unit.Status.Zone.NameServers = slices.Clone(status.NameServers)
	unit.Status.Zone.Provider = nil
	if status.Provider != nil && len(status.Provider.Data.Raw) > 0 {
		unit.Status.Zone.Provider = &dnsv1alpha1.ProviderStatus{Data: status.Provider.Data}
	}
	if status.Provider != nil && len(status.Provider.State.Raw) > 0 {
		if unit.Status.Provider == nil {
			unit.Status.Provider = &dnsv1alpha1.ProviderStatus{}
		}
		unit.Status.Provider.State = status.Provider.State
	} else if unit.Status.Provider != nil {
		unit.Status.Provider.State = runtime.RawExtension{}
		if len(unit.Status.Provider.Data.Raw) == 0 {
			unit.Status.Provider = nil
		}
	}
	unit.Status.Zone.Conditions = slices.Clone(status.Conditions)
}

func route53ZoneFromZoneUnit(unit *dnsv1alpha1.ZoneUnit) dnsv1alpha1.Zone {
	zoneClassNamespace := unit.Spec.Zone.ZoneClassRef.Namespace
	return dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         unit.Spec.Zone.Ref.Namespace,
			Name:              unit.Spec.Zone.Ref.Name,
			UID:               unit.UID,
			Generation:        unit.Spec.Zone.ObservedGeneration,
			DeletionTimestamp: unit.DeletionTimestamp,
		},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName: unit.Spec.Zone.DomainName,
			Provider:   unit.Spec.Provider,
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{
				Namespace: &zoneClassNamespace,
				Name:      unit.Spec.Zone.ZoneClassRef.Name,
			},
			Adoption: unit.Spec.Zone.Adoption,
		},
	}
}

func applyRoute53ZoneStatusFromZoneUnit(zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) {
	if unit.Status.Zone == nil && unit.Status.Provider == nil {
		return
	}
	if unit.Status.Zone != nil {
		zone.Status.NameServers = slices.Clone(unit.Status.Zone.NameServers)
		zone.Status.Conditions = slices.Clone(unit.Status.Zone.Conditions)
	}
	provider := &dnsv1alpha1.ProviderStatus{}
	if unit.Status.Zone != nil && unit.Status.Zone.Provider != nil {
		provider.Data = unit.Status.Zone.Provider.Data
	}
	if unit.Status.Provider != nil {
		provider.State = unit.Status.Provider.State
	}
	if len(provider.Data.Raw) > 0 || len(provider.State.Raw) > 0 {
		zone.Status.Provider = provider
	}
}

func setZoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) {
	status, reason, message := zoneUnitProgrammedCondition(unit)
	setCondition(&unit.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), status, reason, message, unit.Generation)
}

func zoneUnitProgrammedCondition(unit *dnsv1alpha1.ZoneUnit) (metav1.ConditionStatus, string, string) {
	if unit.Status.Zone == nil {
		return metav1.ConditionUnknown, "Reconciling", "ZoneUnit zone status is not observed"
	}
	status := metav1.ConditionTrue
	reason := "Programmed"
	message := "ZoneUnit is programmed"
	if condition := meta.FindStatusCondition(unit.Status.Zone.Conditions, string(dnsv1alpha1.ConditionProgrammed)); condition == nil {
		status = metav1.ConditionUnknown
		reason = "Reconciling"
		message = "ZoneUnit zone Programmed condition is not observed"
	} else if condition.Status == metav1.ConditionFalse {
		return condition.Status, condition.Reason, condition.Message
	} else if condition.Status == metav1.ConditionUnknown {
		status = metav1.ConditionUnknown
		reason = condition.Reason
		message = condition.Message
	}
	for _, item := range unit.Spec.RecordSets {
		if !item.IsAllowed() && !item.DeletionRequested {
			continue
		}
		condition := zoneUnitRecordSetProgrammedCondition(unit, item)
		if condition == nil {
			if status != metav1.ConditionFalse {
				status = metav1.ConditionUnknown
				reason = "Reconciling"
				message = "ZoneUnit record set Programmed condition is not observed"
			}
			continue
		}
		if condition.Status == metav1.ConditionFalse {
			return condition.Status, condition.Reason, condition.Message
		}
		if condition.Status == metav1.ConditionUnknown && status != metav1.ConditionFalse {
			status = metav1.ConditionUnknown
			reason = condition.Reason
			message = condition.Message
		}
	}
	return status, reason, message
}

func zoneUnitRecordSetProgrammedCondition(unit *dnsv1alpha1.ZoneUnit, item dnsv1alpha1.ZoneUnitRecordSetSpec) *metav1.Condition {
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == item.RecordSetNamespace && status.RecordSetName == item.RecordSetName
	})
	if index < 0 {
		return nil
	}
	return meta.FindStatusCondition(unit.Status.RecordSets[index].Conditions, string(dnsv1alpha1.ConditionProgrammed))
}

func (r *ZoneReconciler) patchRoute53ZoneStatus(ctx context.Context, zone *dnsv1alpha1.Zone, mutate func(*route53v1alpha1.Route53ZoneStatusData)) error {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return err
	}
	mutate(&statusData)
	raw, err := json.Marshal(statusData)
	if err != nil {
		return err
	}
	return r.patchZoneStatus(ctx, zone, func(status *dnsv1alpha1.ZoneStatus) {
		publicRaw, _ := json.Marshal(route53v1alpha1.Route53ZoneStatusData{HostedZoneID: statusData.HostedZoneID})
		status.Provider = &dnsv1alpha1.ProviderStatus{
			Data:  runtime.RawExtension{Raw: publicRaw},
			State: runtime.RawExtension{Raw: raw},
		}
	})
}

func (r *ZoneReconciler) removeFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit) error {
	base := unit.DeepCopy()
	unit.Finalizers = slices.DeleteFunc(unit.Finalizers, func(finalizer string) bool {
		return finalizer == ZoneFinalizer
	})
	return client.IgnoreNotFound(r.Patch(ctx, unit, client.MergeFrom(base)))
}

func (r *ZoneReconciler) controllerName() string {
	if r.ControllerName != "" {
		return r.ControllerName
	}
	return DefaultControllerName
}

func (r *ZoneReconciler) providerReference() dnsv1alpha1.ProviderReference {
	return route53ProviderReference(r.ProviderName, r.ProviderVersion)
}

func (r *ZoneReconciler) requeueAfter() time.Duration {
	if r.RequeueAfter != 0 {
		return r.RequeueAfter
	}
	return defaultRoute53ZoneRequeueAfter
}

func (r *ZoneReconciler) changeCheckAfter() time.Duration {
	if r.ChangeCheckAfter != 0 {
		return r.ChangeCheckAfter
	}
	return defaultRoute53ChangeCheckAfter
}

func (r *ZoneReconciler) recordEvent(object runtime.Object, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(object, eventType, reason, message)
}

func setCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
	})
}

func route53ZoneClassParameters(zoneClass *dnsv1alpha1.ZoneClass) (*route53v1alpha1.Route53ZoneClassParameters, error) {
	if len(zoneClass.Spec.Parameters.Raw) == 0 {
		return nil, errors.New("parameters must be an object")
	}
	var params route53v1alpha1.Route53ZoneClassParameters
	if err := json.Unmarshal(zoneClass.Spec.Parameters.Raw, &params); err != nil {
		return nil, fmt.Errorf("parameters must match Route 53 ZoneClass schema: %w", err)
	}
	return &params, nil
}

func route53ZoneStatusData(zone *dnsv1alpha1.Zone) (route53v1alpha1.Route53ZoneStatusData, error) {
	if zone.Status.Provider != nil && len(zone.Status.Provider.State.Raw) > 0 {
		var data route53v1alpha1.Route53ZoneStatusData
		if err := json.Unmarshal(zone.Status.Provider.State.Raw, &data); err != nil {
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("zone status.provider.state must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	if zone.Status.Provider != nil && len(zone.Status.Provider.Data.Raw) > 0 {
		var data route53v1alpha1.Route53ZoneStatusData
		if err := json.Unmarshal(zone.Status.Provider.Data.Raw, &data); err != nil {
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("zone status.provider.data must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	return route53v1alpha1.Route53ZoneStatusData{}, nil
}

func route53ZoneUnitStatusData(unit *dnsv1alpha1.ZoneUnit) (route53v1alpha1.Route53ZoneStatusData, error) {
	if unit.Status.Provider != nil && len(unit.Status.Provider.State.Raw) > 0 {
		var data route53v1alpha1.Route53ZoneStatusData
		if err := json.Unmarshal(unit.Status.Provider.State.Raw, &data); err != nil {
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("ZoneUnit status.provider.state must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	if unit.Status.Zone != nil && unit.Status.Zone.Provider != nil && len(unit.Status.Zone.Provider.Data.Raw) > 0 {
		var data route53v1alpha1.Route53ZoneStatusData
		if err := json.Unmarshal(unit.Status.Zone.Provider.Data.Raw, &data); err != nil {
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("ZoneUnit status.zone.provider.data must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	return route53v1alpha1.Route53ZoneStatusData{}, nil
}

func zoneCreationPolicy(params *route53v1alpha1.Route53ZoneClassParameters) route53v1alpha1.ZoneCreationPolicy {
	if params.ZoneCreationPolicy == "" {
		return route53v1alpha1.ZoneCreationPolicyCreate
	}
	return params.ZoneCreationPolicy
}

func zoneDeletionPolicy(params *route53v1alpha1.Route53ZoneClassParameters) route53v1alpha1.ZoneDeletionPolicy {
	if params.ZoneDeletionPolicy == "" {
		return route53v1alpha1.ZoneDeletionPolicyRetain
	}
	return params.ZoneDeletionPolicy
}

func sameNameZonePolicy(params *route53v1alpha1.Route53ZoneClassParameters) route53v1alpha1.SameNameZonePolicy {
	if params.SameNameZonePolicy == "" {
		return route53v1alpha1.SameNameZonePolicyDeny
	}
	return params.SameNameZonePolicy
}

func namespacePolicyFrom(policy dnsv1alpha1.NamespacePolicy) dnsv1alpha1.NamespacesFrom {
	if policy.From == "" {
		return dnsv1alpha1.NamespacesFromSame
	}
	return policy.From
}

func reservedTagConflict(tags map[string]string) (string, bool) {
	for key := range tags {
		if _, ok := reservedHostedZoneTagKeys[key]; ok {
			return key, true
		}
	}
	return "", false
}

func hostedZoneTags(zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, params *route53v1alpha1.Route53ZoneClassParameters) map[string]string {
	tags := map[string]string{
		"appthrust.io/managed-by":           "dns-api",
		"appthrust.io/zone-namespace":       zone.Namespace,
		"appthrust.io/zone-name":            zone.Name,
		"appthrust.io/zone-class-namespace": zoneClass.Namespace,
		"appthrust.io/zone-class-name":      zoneClass.Name,
	}
	for key, value := range params.Tags {
		tags[key] = value
	}
	return tags
}

func callerReferenceForZone(zone *dnsv1alpha1.Zone) string {
	return "dns-api:" + string(zone.UID)
}

func callerReferenceForZoneState(zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) string {
	if statusData.CallerReference != "" {
		return statusData.CallerReference
	}
	return callerReferenceForZone(zone)
}

func route53ZoneAdoptionID(zone *dnsv1alpha1.Zone) (string, bool, error) {
	if len(zone.Spec.Adoption.Raw) == 0 {
		return "", false, nil
	}

	var adoption struct {
		HostedZoneID string `json:"hostedZoneId"`
	}
	if err := json.Unmarshal(zone.Spec.Adoption.Raw, &adoption); err != nil {
		return "", true, fmt.Errorf("adoption must be an object with hostedZoneId: %w", err)
	}
	if !isRoute53HostedZoneExternalRefID(adoption.HostedZoneID) {
		return "", true, errors.New("adoption.hostedZoneId must be a Route 53 hosted zone ID in Z... form")
	}
	return adoption.HostedZoneID, true, nil
}

func route53ZoneManagedResourceMismatch(zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (string, bool, error) {
	adoptionID, adopting, err := route53ZoneAdoptionID(zone)
	if err != nil || !adopting {
		return "", false, err
	}
	statusID := normalizeHostedZoneID(statusData.HostedZoneID)
	if statusID == "" || statusID == adoptionID {
		return "", false, nil
	}
	return "spec.adoption points to a different Route 53 hosted zone than the managed resource recorded in status", true, nil
}

func hostedZoneIDForDelete(zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (string, error) {
	if hostedZoneID := normalizeHostedZoneID(statusData.HostedZoneID); hostedZoneID != "" {
		return hostedZoneID, nil
	}
	if adoptionID, adopting, err := route53ZoneAdoptionID(zone); err != nil {
		return "", err
	} else if adopting {
		return adoptionID, nil
	}
	return "", nil
}

func isRoute53HostedZoneExternalRefID(id string) bool {
	if len(id) < 2 || id[0] != 'Z' || strings.Contains(id, "/") {
		return false
	}
	for _, r := range id[1:] {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func hostedZoneSpecMismatch(zone *dnsv1alpha1.Zone, params *route53v1alpha1.Route53ZoneClassParameters, hostedZone HostedZone) string {
	if hostedZone.Name != zone.Spec.DomainName {
		return "Route 53 hosted zone name does not match Zone domainName"
	}
	if hostedZone.Private {
		return "Route 53 hosted zone is private; only public hosted zones are supported"
	}
	return ""
}

func normalizeHostedZoneID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/hostedzone/")
	return strings.TrimPrefix(id, "hostedzone/")
}

func normalizeDomainName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	return strings.TrimSuffix(name, ".")
}

func normalizeRoute53RecordName(name string) string {
	name = strings.TrimSpace(name)
	name = unescapeRoute53RecordName(name)
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func unescapeRoute53RecordName(name string) string {
	var out strings.Builder
	out.Grow(len(name))
	for index := 0; index < len(name); index++ {
		if name[index] == '\\' && index+3 < len(name) &&
			isOctalDigit(name[index+1]) &&
			isOctalDigit(name[index+2]) &&
			isOctalDigit(name[index+3]) {
			value := (name[index+1]-'0')*64 + (name[index+2]-'0')*8 + (name[index+3] - '0')
			out.WriteByte(value)
			index += 3
			continue
		}
		out.WriteByte(name[index])
	}
	return out.String()
}

func isOctalDigit(value byte) bool {
	return value >= '0' && value <= '7'
}

func sameHostedZoneID(a, b string) bool {
	return normalizeHostedZoneID(a) == normalizeHostedZoneID(b)
}

func providerErrorCondition(err error) (string, string) {
	reason := providerErrorReason(err)

	var reasonErr *providerReasonError
	if errors.As(err, &reasonErr) {
		return reason, reasonErr.message
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		message := strings.TrimSpace(apiErr.ErrorMessage())
		if message == "" {
			return reason, apiErr.ErrorCode()
		}
		return reason, apiErr.ErrorCode() + ": " + message
	}

	return reason, err.Error()
}

func providerErrorReason(err error) string {
	var reasonErr *providerReasonError
	if errors.As(err, &reasonErr) {
		return reasonErr.reason
	}

	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return "ReconcileError"
	}

	switch apiErr.ErrorCode() {
	case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation", "UnrecognizedClientException", "InvalidClientTokenId", "ExpiredToken", "InvalidGrantException":
		return "ProviderAccessDenied"
	case "HostedZoneAlreadyExists", "HostedZoneNotEmpty", "ConflictingDomainExists":
		return "ProviderConflict"
	case "InvalidInput", "InvalidDomainName", "NoSuchHostedZone", "NoSuchChange", "InvalidChangeBatch":
		return "ProviderInvalidRequest"
	case "Throttling", "ThrottlingException", "PriorRequestNotComplete", "ServiceUnavailable":
		return "ProviderUnavailable"
	default:
		return "ProviderUnavailable"
	}
}

func isProviderNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchHostedZone"
}
