package endpointrecordset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	endpointconversionv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/conversion/v1alpha1"
	endpointv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/endpoint/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	fieldOwner                               = "dns-api-endpoint-controller"
	managedByLabel                           = "app.kubernetes.io/managed-by"
	managedByValue                           = "dns-api-endpoint"
	generatedLabelEndpointRecordSetNamespace = "endpoint.dns.appthrust.io/endpointrecordset-namespace"
	generatedLabelEndpointRecordSetName      = "endpoint.dns.appthrust.io/endpointrecordset-name"
)

type Reconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	RESTConfig         *rest.Config
	RecordSetNamespace string
}

// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointrecordsets/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=endpoint.dns.appthrust.io,resources=endpointprovidercapabilities,verbs=get;list;watch
// +kubebuilder:rbac:groups=endpoint.route53.dns.appthrust.io,resources=endpointrecordsetconversions,verbs=create
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zones,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=recordsets,verbs=create;delete;get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	recordSetNamespace := r.RecordSetNamespace
	if recordSetNamespace == "" {
		recordSetNamespace = req.Namespace
	}
	var endpointRecordSet endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, req.NamespacedName, &endpointRecordSet); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.cleanupForEndpointRecordSet(ctx, recordSetNamespace, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}
	if !endpointRecordSet.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.cleanupForEndpointRecordSet(ctx, recordSetNamespace, endpointRecordSet.Namespace, endpointRecordSet.Name)
	}

	status, desired, err := r.buildStatusAndRecordSets(ctx, &endpointRecordSet, recordSetNamespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyRecordSets(ctx, &endpointRecordSet, recordSetNamespace, desired); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.patchStatus(ctx, &endpointRecordSet, status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("endpoint-recordset").
		For(&endpointv1alpha1.EndpointRecordSet{}).
		Watches(&dnsv1alpha1.Zone{}, handler.EnqueueRequestsFromMapFunc(r.mapAllEndpointRecordSets)).
		Watches(&endpointv1alpha1.EndpointProviderCapability{}, handler.EnqueueRequestsFromMapFunc(r.mapAllEndpointRecordSets)).
		Watches(&dnsv1alpha1.RecordSet{}, handler.EnqueueRequestsFromMapFunc(r.mapRecordSetToEndpointRecordSet)).
		Complete(r)
}

func (r *Reconciler) mapAllEndpointRecordSets(ctx context.Context, _ client.Object) []reconcile.Request {
	var list endpointv1alpha1.EndpointRecordSetList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, item := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&item)})
	}
	return requests
}

func (r *Reconciler) mapRecordSetToEndpointRecordSet(_ context.Context, obj client.Object) []reconcile.Request {
	namespace := obj.GetLabels()[generatedLabelEndpointRecordSetNamespace]
	name := obj.GetLabels()[generatedLabelEndpointRecordSetName]
	if namespace == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
}

func (r *Reconciler) buildStatusAndRecordSets(ctx context.Context, endpointRecordSet *endpointv1alpha1.EndpointRecordSet, recordSetNamespace string) (endpointv1alpha1.EndpointRecordSetStatus, []dnsv1alpha1.RecordSet, error) {
	status := endpointv1alpha1.EndpointRecordSetStatus{ObservedGeneration: endpointRecordSet.Generation}
	desiredByName := map[string]dnsv1alpha1.RecordSet{}
	for _, hostname := range sortedStrings(endpointRecordSet.Spec.Hostnames) {
		hostnameStatus, recordSets, err := r.recordSetsForHostname(ctx, endpointRecordSet, strings.TrimSuffix(hostname, "."), recordSetNamespace)
		if err != nil {
			return status, nil, err
		}
		status.Hostnames = append(status.Hostnames, hostnameStatus)
		for _, recordSet := range recordSets {
			desiredByName[recordSet.Name] = recordSet
		}
	}
	status.HostnameCount = int32(len(status.Hostnames))
	if len(status.Hostnames) == 0 {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionFalse, ObservedGeneration: endpointRecordSet.Generation, Reason: "NoHostname", Message: "EndpointRecordSet has no hostnames."})
		return status, nil, nil
	}
	allResolved := true
	for _, hostnameStatus := range status.Hostnames {
		condition := meta.FindStatusCondition(hostnameStatus.Conditions, "Resolved")
		if condition == nil || condition.Status != metav1.ConditionTrue {
			allResolved = false
			break
		}
	}
	if allResolved {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionTrue, ObservedGeneration: endpointRecordSet.Generation, Reason: "Resolved", Message: "All hostnames resolved to generated RecordSets."})
	} else {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionFalse, ObservedGeneration: endpointRecordSet.Generation, Reason: "HostnameNotResolved", Message: "At least one hostname could not be resolved."})
	}
	desired := make([]dnsv1alpha1.RecordSet, 0, len(desiredByName))
	for _, recordSet := range desiredByName {
		desired = append(desired, recordSet)
	}
	sort.Slice(desired, func(i, j int) bool {
		return desired[i].Name < desired[j].Name
	})
	return status, desired, nil
}

func (r *Reconciler) recordSetsForHostname(ctx context.Context, endpointRecordSet *endpointv1alpha1.EndpointRecordSet, hostname, recordSetNamespace string) (endpointv1alpha1.EndpointRecordSetHostnameStatus, []dnsv1alpha1.RecordSet, error) {
	status := endpointv1alpha1.EndpointRecordSetHostnameStatus{Hostname: hostname}
	zones, err := r.sortedZones(ctx, hostname)
	if err != nil {
		return status, nil, err
	}
	if len(zones) == 0 {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "ZoneNotResolved", Message: "No Zone suffix matched this hostname."})
		return status, nil, nil
	}
	rejections := make([]string, 0, len(zones))
	for _, zone := range zones {
		capability, capabilityCount, err := r.capabilityForZone(ctx, &zone)
		if err != nil {
			return status, nil, err
		}
		if capabilityCount == 0 {
			rejections = append(rejections, fmt.Sprintf("%s/%s: no EndpointProviderCapability for provider %s/%s", zone.Namespace, zone.Name, zone.Spec.Provider.Name, zone.Spec.Provider.Version))
			continue
		}
		if capabilityCount > 1 {
			rejections = append(rejections, fmt.Sprintf("%s/%s: multiple EndpointProviderCapability resources for provider %s/%s", zone.Namespace, zone.Name, zone.Spec.Provider.Name, zone.Spec.Provider.Version))
			continue
		}
		input := endpointv1alpha1.EndpointRecordSetConversionInput{
			Hostname: hostname,
			Name:     relativeRecordName(hostname, zone.Spec.DomainName),
			Zone:     endpointv1alpha1.EndpointRecordSetConversionZone{DomainName: zone.Spec.DomainName},
			Targets:  endpointRecordSet.Spec.Targets,
		}
		fragments, message, err := r.convertEndpointRecordSet(ctx, capability, input)
		if err != nil {
			return status, nil, err
		}
		if message != "" {
			rejections = append(rejections, fmt.Sprintf("%s/%s: endpoint conversion failed: %s", zone.Namespace, zone.Name, message))
			continue
		}
		recordSets := make([]dnsv1alpha1.RecordSet, 0, len(fragments))
		allowed := true
		rejectionMessage := ""
		for _, fragment := range fragments {
			recordSet := r.recordSetFromFragment(ctx, endpointRecordSet, recordSetNamespace, &zone, fragment)
			ok, message := declaration.RecordSetAllowedByZone(ctx, r.Client, &recordSet, &zone)
			if !ok {
				allowed = false
				rejectionMessage = fmt.Sprintf("%s/%s: generated RecordSet %s/%s is not allowed by Zone: %s", zone.Namespace, zone.Name, recordSet.Namespace, recordSet.Name, message)
				break
			}
			recordSets = append(recordSets, recordSet)
		}
		if !allowed {
			rejections = append(rejections, rejectionMessage)
			continue
		}
		status.Zone = &endpointv1alpha1.EndpointRecordSetZoneStatus{
			Ref:        dnsv1alpha1.ObjectReference{Namespace: zone.Namespace, Name: zone.Name},
			DomainName: zone.Spec.DomainName,
			Provider:   zone.Spec.Provider,
		}
		for _, recordSet := range recordSets {
			item := endpointv1alpha1.EndpointRecordSetGeneratedRecordSetStatus{
				Ref:  dnsv1alpha1.ObjectReference{Namespace: recordSet.Namespace, Name: recordSet.Name},
				Name: recordSet.Spec.Name,
				Type: endpointv1alpha1.EndpointRecordSetType(recordSet.Spec.Type),
				Fragment: &endpointv1alpha1.RecordSetSpecFragment{
					Type:    endpointv1alpha1.EndpointRecordSetType(recordSet.Spec.Type),
					Name:    recordSet.Spec.Name,
					TTL:     recordSet.Spec.TTL,
					A:       recordSet.Spec.A,
					AAAA:    recordSet.Spec.AAAA,
					CNAME:   recordSet.Spec.CNAME,
					Options: recordSet.Spec.Options,
				},
			}
			var existing dnsv1alpha1.RecordSet
			if err := r.Get(ctx, client.ObjectKey{Namespace: recordSet.Namespace, Name: recordSet.Name}, &existing); err == nil {
				item.Conditions = existing.Status.Conditions
			}
			status.RecordSets = append(status.RecordSets, item)
		}
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionTrue, Reason: "Resolved", Message: "Hostname resolved to generated RecordSets."})
		return status, recordSets, nil
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "ZoneNotResolved", Message: "No Zone and EndpointProviderCapability combination accepted this hostname: " + strings.Join(rejections, "; ")})
	return status, nil, nil
}

func (r *Reconciler) convertEndpointRecordSet(ctx context.Context, capability *endpointv1alpha1.EndpointProviderCapability, input endpointv1alpha1.EndpointRecordSetConversionInput) ([]endpointv1alpha1.RecordSetSpecFragment, string, error) {
	if r.RESTConfig == nil {
		return nil, "EndpointRecordSet conversion API is configured but no REST config is available", nil
	}
	version := capability.Spec.Conversion.Version
	if version == "" {
		version = capability.Spec.Provider.Version
	}
	gvr := schema.GroupVersionResource{
		Group:    capability.Spec.Conversion.Group,
		Version:  version,
		Resource: capability.Spec.Conversion.Resource,
	}
	uid := string(uuid.NewUUID())
	request := endpointconversionv1alpha1.EndpointRecordSetConversion{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gvr.Group + "/" + gvr.Version,
			Kind:       "EndpointRecordSetConversion",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "endpoint-record-set-conversion-" + uid,
		},
		Spec: endpointconversionv1alpha1.EndpointRecordSetConversionSpec{
			UID:   uid,
			Input: input,
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&request)
	if err != nil {
		return nil, "", err
	}
	object["apiVersion"] = gvr.Group + "/" + gvr.Version
	object["kind"] = "EndpointRecordSetConversion"
	dynamicClient, err := dynamic.NewForConfig(r.RESTConfig)
	if err != nil {
		return nil, "", err
	}
	responseObject, err := dynamicClient.Resource(gvr).Create(ctx, &unstructured.Unstructured{Object: object}, metav1.CreateOptions{})
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return nil, "EndpointRecordSet conversion API denied the request: " + err.Error(), nil
	}
	if err != nil {
		return nil, "EndpointRecordSet conversion API is unavailable: " + err.Error(), nil
	}
	var response endpointconversionv1alpha1.EndpointRecordSetConversion
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(responseObject.Object, &response); err != nil {
		return nil, "EndpointRecordSet conversion API returned malformed content: " + err.Error(), nil
	}
	if response.Status.UID != uid {
		return nil, "EndpointRecordSet conversion API returned mismatched uid", nil
	}
	if response.Status.Result.Status == "Failure" {
		message := response.Status.Result.Message
		if message == "" {
			message = response.Status.Result.Reason
		}
		return nil, message, nil
	}
	if response.Status.Result.Status != "Success" {
		return nil, "EndpointRecordSet conversion API returned unsupported result status " + response.Status.Result.Status, nil
	}
	if response.Status.Output == nil || len(response.Status.Output.Fragments) == 0 {
		return nil, "EndpointRecordSet conversion API returned no fragments", nil
	}
	for _, fragment := range response.Status.Output.Fragments {
		if fragment.Name != input.Name {
			return nil, "EndpointRecordSet conversion API changed record name", nil
		}
		if fragment.Type != endpointv1alpha1.EndpointRecordSetTypeA && fragment.Type != endpointv1alpha1.EndpointRecordSetTypeAAAA && fragment.Type != endpointv1alpha1.EndpointRecordSetTypeCNAME {
			return nil, "EndpointRecordSet conversion API returned unsupported record type " + string(fragment.Type), nil
		}
	}
	return response.Status.Output.Fragments, "", nil
}

func (r *Reconciler) sortedZones(ctx context.Context, hostname string) ([]dnsv1alpha1.Zone, error) {
	var list dnsv1alpha1.ZoneList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	zones := make([]dnsv1alpha1.Zone, 0, len(list.Items))
	for _, zone := range list.Items {
		if hostname == zone.Spec.DomainName || strings.HasSuffix(hostname, "."+zone.Spec.DomainName) {
			zones = append(zones, zone)
		}
	}
	sort.SliceStable(zones, func(i, j int) bool {
		return len(zones[i].Spec.DomainName) > len(zones[j].Spec.DomainName)
	})
	return zones, nil
}

func (r *Reconciler) capabilityForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*endpointv1alpha1.EndpointProviderCapability, int, error) {
	var list endpointv1alpha1.EndpointProviderCapabilityList
	if err := r.List(ctx, &list); err != nil {
		return nil, 0, err
	}
	var matched []endpointv1alpha1.EndpointProviderCapability
	for _, item := range list.Items {
		if item.Spec.Provider == zone.Spec.Provider {
			matched = append(matched, item)
		}
	}
	if len(matched) != 1 {
		return nil, len(matched), nil
	}
	return &matched[0], 1, nil
}

func (r *Reconciler) recordSetFromFragment(ctx context.Context, endpointRecordSet *endpointv1alpha1.EndpointRecordSet, namespace string, zone *dnsv1alpha1.Zone, fragment endpointv1alpha1.RecordSetSpecFragment) dnsv1alpha1.RecordSet {
	recordSet := dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      generatedRecordSetName(endpointRecordSet.Namespace, endpointRecordSet.Name, zone.Namespace, zone.Name, fragment.Name, fragment.Type),
			Labels: map[string]string{
				managedByLabel:                           managedByValue,
				generatedLabelEndpointRecordSetNamespace: endpointRecordSet.Namespace,
				generatedLabelEndpointRecordSetName:      endpointRecordSet.Name,
			},
		},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Namespace: &zone.Namespace, Name: zone.Name},
			Provider: zone.Spec.Provider,
			Type:     fragment.Type.DNSRecordType(),
			Name:     fragment.Name,
			TTL:      fragment.TTL,
			A:        fragment.A,
			AAAA:     fragment.AAAA,
			CNAME:    fragment.CNAME,
			Options:  fragment.Options,
		},
	}
	return recordSet
}

func generatedRecordSetName(endpointRecordSetNamespace, endpointRecordSetName, zoneNamespace, zoneName, recordName string, recordType endpointv1alpha1.EndpointRecordSetType) string {
	hashInput := endpointRecordSetNamespace + "/" + endpointRecordSetName + "/" + zoneNamespace + "/" + zoneName + "/" + recordName
	sum := sha256.Sum256([]byte(hashInput))
	hash := hex.EncodeToString(sum[:])[:10]
	cleanRecordName := strings.NewReplacer(".", "-", "*", "wildcard").Replace(recordName)
	candidate := fmt.Sprintf("%s-%s-%s-%s", endpointRecordSetName, cleanRecordName, strings.ToLower(string(recordType)), hash)
	if len(candidate) <= 63 {
		return candidate
	}
	suffix := "-" + strings.ToLower(string(recordType)) + "-" + hash
	prefixLength := 63 - len(suffix)
	return strings.Trim(candidate[:prefixLength], "-") + suffix
}

func (r *Reconciler) applyRecordSets(ctx context.Context, endpointRecordSet *endpointv1alpha1.EndpointRecordSet, namespace string, desired []dnsv1alpha1.RecordSet) error {
	desiredNames := map[string]struct{}{}
	for _, item := range desired {
		desired := item
		desiredNames[desired.Name] = struct{}{}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var existing dnsv1alpha1.RecordSet
			err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
			if apierrors.IsNotFound(err) {
				return r.Create(ctx, &desired, client.FieldOwner(fieldOwner))
			}
			if err != nil {
				return err
			}
			existing.Labels = desired.Labels
			existing.Spec = desired.Spec
			return r.Update(ctx, &existing, client.FieldOwner(fieldOwner))
		}); err != nil {
			return err
		}
	}
	var existing dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(namespace), client.MatchingLabels{
		managedByLabel:                           managedByValue,
		generatedLabelEndpointRecordSetNamespace: endpointRecordSet.Namespace,
		generatedLabelEndpointRecordSetName:      endpointRecordSet.Name,
	}); err != nil {
		return err
	}
	for _, item := range existing.Items {
		if _, ok := desiredNames[item.Name]; ok {
			continue
		}
		item := item
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) cleanupForEndpointRecordSet(ctx context.Context, recordSetNamespace, endpointRecordSetNamespace, endpointRecordSetName string) error {
	var existing dnsv1alpha1.RecordSetList
	if err := r.List(ctx, &existing, client.InNamespace(recordSetNamespace), client.MatchingLabels{
		managedByLabel:                           managedByValue,
		generatedLabelEndpointRecordSetNamespace: endpointRecordSetNamespace,
		generatedLabelEndpointRecordSetName:      endpointRecordSetName,
	}); err != nil {
		return err
	}
	for _, item := range existing.Items {
		item := item
		if err := r.Delete(ctx, &item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *Reconciler) patchStatus(ctx context.Context, endpointRecordSet *endpointv1alpha1.EndpointRecordSet, status endpointv1alpha1.EndpointRecordSetStatus) error {
	var current endpointv1alpha1.EndpointRecordSet
	if err := r.Get(ctx, client.ObjectKeyFromObject(endpointRecordSet), &current); err != nil {
		return err
	}
	base := current.DeepCopy()
	current.Status = status
	return client.IgnoreNotFound(r.Status().Patch(ctx, &current, client.MergeFrom(base)))
}

func relativeRecordName(hostname, zoneDomainName string) string {
	trimmed := strings.TrimSuffix(hostname, ".")
	zone := strings.TrimSuffix(zoneDomainName, ".")
	if trimmed == zone {
		return "@"
	}
	return strings.TrimSuffix(trimmed, "."+zone)
}

func sortedStrings(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}
