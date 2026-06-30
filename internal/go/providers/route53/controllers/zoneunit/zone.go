package route53

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsroute53 "github.com/aws/aws-sdk-go-v2/service/route53"
	awstypes "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	DefaultControllerName = "route53.dns.appthrust.io/controller"

	ZoneFinalizer = "route53.dns.appthrust.io/zoneunit-finalizer"

	defaultRoute53ZoneRequeueAfter   = 10 * time.Minute
	defaultRoute53ChangeCheckAfter   = 10 * time.Second
	route53RecordSetChangeBatchLimit = 1000
)

var reservedHostedZoneTagKeys = map[string]struct{}{
	"appthrust.io/managed-by":           {},
	"appthrust.io/zone-namespace":       {},
	"appthrust.io/zone-name":            {},
	"appthrust.io/zone-class-namespace": {},
	"appthrust.io/zone-class-name":      {},
}

// HostedZone is the provider-neutral part of a Route 53 hosted zone used by the reconciler.
type HostedZone struct {
	ID              string
	Name            string
	CallerReference string
	Private         bool
	NameServers     []string
}

// CreatedHostedZone is returned after Route 53 accepts a hosted zone creation request.
type CreatedHostedZone struct {
	HostedZone HostedZone
	Change     *route53v1alpha1.Route53Change
}

type RecordSetResource struct {
	HostedZoneID string
	Name         string
	Type         dnsv1alpha1.RecordType
	TTL          *int64
	Values       []string
	Alias        *route53v1alpha1.Route53AliasTarget
}

type RecordSetChangeAction string

const (
	RecordSetChangeActionUpsert RecordSetChangeAction = "UPSERT"
	RecordSetChangeActionDelete RecordSetChangeAction = "DELETE"
)

type RecordSetChange struct {
	Action    RecordSetChangeAction
	RecordSet RecordSetResource
}

// Provider wraps Route 53 calls so the controller logic can be tested without AWS.
type Provider interface {
	GetHostedZone(ctx context.Context, id string) (HostedZone, error)
	ListHostedZonesByName(ctx context.Context, domainName string) ([]HostedZone, error)
	CreateHostedZone(ctx context.Context, domainName, callerReference string) (CreatedHostedZone, error)
	GetChange(ctx context.Context, id string) (*route53v1alpha1.Route53Change, error)
	DeleteHostedZone(ctx context.Context, id string) (*route53v1alpha1.Route53Change, error)
	TagHostedZone(ctx context.Context, id string, tags map[string]string) error
	ListRecordSets(ctx context.Context, hostedZoneID string) ([]RecordSetResource, error)
	ChangeRecordSets(ctx context.Context, hostedZoneID string, changes []RecordSetChange) (*route53v1alpha1.Route53Change, error)
}

type ProviderFactory interface {
	ProviderForIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (Provider, error)
}

type awsRoute53API interface {
	GetHostedZone(context.Context, *awsroute53.GetHostedZoneInput, ...func(*awsroute53.Options)) (*awsroute53.GetHostedZoneOutput, error)
	ListHostedZonesByName(context.Context, *awsroute53.ListHostedZonesByNameInput, ...func(*awsroute53.Options)) (*awsroute53.ListHostedZonesByNameOutput, error)
	CreateHostedZone(context.Context, *awsroute53.CreateHostedZoneInput, ...func(*awsroute53.Options)) (*awsroute53.CreateHostedZoneOutput, error)
	GetChange(context.Context, *awsroute53.GetChangeInput, ...func(*awsroute53.Options)) (*awsroute53.GetChangeOutput, error)
	DeleteHostedZone(context.Context, *awsroute53.DeleteHostedZoneInput, ...func(*awsroute53.Options)) (*awsroute53.DeleteHostedZoneOutput, error)
	ChangeTagsForResource(context.Context, *awsroute53.ChangeTagsForResourceInput, ...func(*awsroute53.Options)) (*awsroute53.ChangeTagsForResourceOutput, error)
	ListResourceRecordSets(context.Context, *awsroute53.ListResourceRecordSetsInput, ...func(*awsroute53.Options)) (*awsroute53.ListResourceRecordSetsOutput, error)
	ChangeResourceRecordSets(context.Context, *awsroute53.ChangeResourceRecordSetsInput, ...func(*awsroute53.Options)) (*awsroute53.ChangeResourceRecordSetsOutput, error)
}

// AWSProvider calls Route 53 through the AWS SDK.
type AWSProvider struct {
	client awsRoute53API
}

func NewAWSProvider(client awsRoute53API) *AWSProvider {
	return &AWSProvider{client: client}
}

func NewRoute53Client(config aws.Config) *awsroute53.Client {
	return awsroute53.NewFromConfig(config)
}

func (p *AWSProvider) GetHostedZone(ctx context.Context, id string) (HostedZone, error) {
	out, err := p.client.GetHostedZone(ctx, &awsroute53.GetHostedZoneInput{
		Id: aws.String(normalizeHostedZoneID(id)),
	})
	if err != nil {
		return HostedZone{}, err
	}

	return hostedZoneFromAWS(out.HostedZone, out.DelegationSet), nil
}

func (p *AWSProvider) ListHostedZonesByName(ctx context.Context, domainName string) ([]HostedZone, error) {
	var zones []HostedZone
	dnsName := aws.String(domainName)
	var hostedZoneID *string

	for {
		out, err := p.client.ListHostedZonesByName(ctx, &awsroute53.ListHostedZonesByNameInput{
			DNSName:      dnsName,
			HostedZoneId: hostedZoneID,
			MaxItems:     aws.Int32(100),
		})
		if err != nil {
			return nil, err
		}

		for _, zone := range out.HostedZones {
			if normalizeDomainName(aws.ToString(zone.Name)) == domainName {
				zones = append(zones, hostedZoneFromAWS(&zone, nil))
			}
		}

		if !out.IsTruncated {
			return zones, nil
		}
		dnsName = out.NextDNSName
		hostedZoneID = out.NextHostedZoneId
	}
}

func (p *AWSProvider) CreateHostedZone(ctx context.Context, domainName, callerReference string) (CreatedHostedZone, error) {
	out, err := p.client.CreateHostedZone(ctx, &awsroute53.CreateHostedZoneInput{
		Name:            aws.String(domainName),
		CallerReference: aws.String(callerReference),
	})
	if err != nil {
		return CreatedHostedZone{}, err
	}

	return CreatedHostedZone{
		HostedZone: hostedZoneFromAWS(out.HostedZone, out.DelegationSet),
		Change:     changeFromAWS(out.ChangeInfo),
	}, nil
}

func (p *AWSProvider) GetChange(ctx context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	out, err := p.client.GetChange(ctx, &awsroute53.GetChangeInput{
		Id: aws.String(id),
	})
	if err != nil {
		return nil, err
	}
	return changeFromAWS(out.ChangeInfo), nil
}

func (p *AWSProvider) DeleteHostedZone(ctx context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	out, err := p.client.DeleteHostedZone(ctx, &awsroute53.DeleteHostedZoneInput{
		Id: aws.String(normalizeHostedZoneID(id)),
	})
	if err != nil {
		return nil, err
	}
	return changeFromAWS(out.ChangeInfo), nil
}

func (p *AWSProvider) TagHostedZone(ctx context.Context, id string, tags map[string]string) error {
	addTags := make([]awstypes.Tag, 0, len(tags))
	for key, value := range tags {
		addTags = append(addTags, awstypes.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		})
	}

	_, err := p.client.ChangeTagsForResource(ctx, &awsroute53.ChangeTagsForResourceInput{
		ResourceType: awstypes.TagResourceTypeHostedzone,
		ResourceId:   aws.String(normalizeHostedZoneID(id)),
		AddTags:      addTags,
	})
	return err
}

func (p *AWSProvider) ListRecordSets(ctx context.Context, hostedZoneID string) ([]RecordSetResource, error) {
	var records []RecordSetResource
	var startName *string
	var startType awstypes.RRType
	var startIdentifier *string

	for {
		input := &awsroute53.ListResourceRecordSetsInput{
			HostedZoneId: aws.String(normalizeHostedZoneID(hostedZoneID)),
			MaxItems:     aws.Int32(300),
		}
		if startName != nil {
			input.StartRecordName = startName
			input.StartRecordType = startType
			input.StartRecordIdentifier = startIdentifier
		}
		out, err := p.client.ListResourceRecordSets(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, recordSet := range out.ResourceRecordSets {
			if record := recordSetFromAWS(hostedZoneID, recordSet); record != nil {
				records = append(records, *record)
			}
		}
		if !out.IsTruncated {
			return records, nil
		}
		startName = out.NextRecordName
		startType = out.NextRecordType
		startIdentifier = out.NextRecordIdentifier
	}
}

func (p *AWSProvider) ChangeRecordSets(ctx context.Context, hostedZoneID string, changes []RecordSetChange) (*route53v1alpha1.Route53Change, error) {
	awsChanges := make([]awstypes.Change, 0, len(changes))
	for _, change := range changes {
		action := awstypes.ChangeActionUpsert
		if change.Action == RecordSetChangeActionDelete {
			action = awstypes.ChangeActionDelete
		}
		awsRecordSet := recordSetToAWS(change.RecordSet)
		awsChanges = append(awsChanges, awstypes.Change{
			Action:            action,
			ResourceRecordSet: &awsRecordSet,
		})
	}

	out, err := p.client.ChangeResourceRecordSets(ctx, &awsroute53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(normalizeHostedZoneID(hostedZoneID)),
		ChangeBatch: &awstypes.ChangeBatch{
			Changes: awsChanges,
		},
	})
	if err != nil {
		return nil, err
	}
	return changeFromAWS(out.ChangeInfo), nil
}

func (p *AWSProvider) GetRecordSet(ctx context.Context, hostedZoneID, recordName string, recordType dnsv1alpha1.RecordType) (*RecordSetResource, error) {
	out, err := p.client.ListResourceRecordSets(ctx, &awsroute53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(normalizeHostedZoneID(hostedZoneID)),
		StartRecordName: aws.String(recordName),
		StartRecordType: awstypes.RRType(recordType),
		MaxItems:        aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(out.ResourceRecordSets) == 0 {
		return nil, nil
	}
	recordSet := out.ResourceRecordSets[0]
	if normalizeRoute53RecordName(aws.ToString(recordSet.Name)) != normalizeRoute53RecordName(recordName) || string(recordSet.Type) != string(recordType) {
		return nil, nil
	}
	return recordSetFromAWS(hostedZoneID, recordSet), nil
}

func (p *AWSProvider) UpsertRecordSet(ctx context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	return p.ChangeRecordSets(ctx, recordSet.HostedZoneID, []RecordSetChange{{Action: RecordSetChangeActionUpsert, RecordSet: recordSet}})
}

func (p *AWSProvider) DeleteRecordSet(ctx context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	return p.ChangeRecordSets(ctx, recordSet.HostedZoneID, []RecordSetChange{{Action: RecordSetChangeActionDelete, RecordSet: recordSet}})
}

func hostedZoneFromAWS(zone *awstypes.HostedZone, delegationSet *awstypes.DelegationSet) HostedZone {
	if zone == nil {
		return HostedZone{}
	}

	hostedZone := HostedZone{
		ID:              normalizeHostedZoneID(aws.ToString(zone.Id)),
		Name:            normalizeDomainName(aws.ToString(zone.Name)),
		CallerReference: aws.ToString(zone.CallerReference),
	}
	if zone.Config != nil && zone.Config.PrivateZone {
		hostedZone.Private = true
	}
	if delegationSet != nil {
		hostedZone.NameServers = slices.Clone(delegationSet.NameServers)
	}
	return hostedZone
}

func recordSetFromAWS(hostedZoneID string, recordSet awstypes.ResourceRecordSet) *RecordSetResource {
	out := &RecordSetResource{
		HostedZoneID: normalizeHostedZoneID(hostedZoneID),
		Name:         normalizeRoute53RecordName(aws.ToString(recordSet.Name)),
		Type:         dnsv1alpha1.RecordType(recordSet.Type),
		TTL:          recordSet.TTL,
	}
	for _, record := range recordSet.ResourceRecords {
		out.Values = append(out.Values, aws.ToString(record.Value))
	}
	if recordSet.AliasTarget != nil {
		out.Alias = &route53v1alpha1.Route53AliasTarget{
			DNSName:              aws.ToString(recordSet.AliasTarget.DNSName),
			HostedZoneID:         aws.ToString(recordSet.AliasTarget.HostedZoneId),
			EvaluateTargetHealth: recordSet.AliasTarget.EvaluateTargetHealth,
		}
	}
	return out
}

func recordSetToAWS(recordSet RecordSetResource) awstypes.ResourceRecordSet {
	out := awstypes.ResourceRecordSet{
		Name: aws.String(recordSet.Name),
		Type: awstypes.RRType(recordSet.Type),
		TTL:  recordSet.TTL,
	}
	for _, value := range recordSet.Values {
		out.ResourceRecords = append(out.ResourceRecords, awstypes.ResourceRecord{Value: aws.String(value)})
	}
	if recordSet.Alias != nil {
		out.TTL = nil
		out.ResourceRecords = nil
		out.AliasTarget = &awstypes.AliasTarget{
			DNSName:              aws.String(recordSet.Alias.DNSName),
			HostedZoneId:         aws.String(recordSet.Alias.HostedZoneID),
			EvaluateTargetHealth: recordSet.Alias.EvaluateTargetHealth,
		}
	}
	return out
}

func changeFromAWS(change *awstypes.ChangeInfo) *route53v1alpha1.Route53Change {
	if change == nil {
		return nil
	}

	out := &route53v1alpha1.Route53Change{
		ID:     aws.ToString(change.Id),
		Status: route53v1alpha1.Route53ChangeStatus(change.Status),
	}
	if change.SubmittedAt != nil {
		out.SubmittedAt = metav1.NewTime(*change.SubmittedAt)
	}
	return out
}

// ZoneReconciler reconciles core Zone objects for Route 53 public hosted zones.
type ZoneReconciler struct {
	client.Client

	Scheme           *runtime.Scheme
	Provider         Provider
	ProviderFactory  ProviderFactory
	ControllerName   string
	ProviderName     string
	ProviderVersion  string
	RequeueAfter     time.Duration
	ChangeCheckAfter time.Duration
	Recorder         record.EventRecorder
}

// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits,verbs=delete;get;list;watch;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneunits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=zoneclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=dns.appthrust.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=route53.dns.appthrust.io,resources=route53identities,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, req.NamespacedName, &unit); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if unit.Spec.Provider.Name != r.providerReference().Name {
		return ctrl.Result{}, nil
	}
	zone := route53ZoneFromZoneUnit(&unit)
	applyRoute53ZoneStatusFromZoneUnit(&zone, &unit)

	if !unit.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &zone, &unit)
	}

	statusData, err := route53ZoneStatusData(&zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if message, mismatch, err := route53ZoneManagedResourceMismatch(&zone, statusData); err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if mismatch {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "ManagedResourceMismatch", message)
	}

	zoneClass, params, identity, accepted, err := r.acceptZone(ctx, &zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if !isReadyCurrent(identity.Status.Conditions, identity.Generation) {
		if err := r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced Route53Identity is not Ready"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
	}

	provider, err := r.providerForIdentity(ctx, identity)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}

	if updated, err := r.reconcileFinalizer(ctx, &unit, params); err != nil {
		return ctrl.Result{}, err
	} else if updated {
		return ctrl.Result{Requeue: true}, nil
	}

	if result, done, err := r.refreshPendingHostedZoneChange(ctx, provider, &zone, statusData); done || err != nil {
		return result, err
	}
	selection, done, result, err := r.selectHostedZone(ctx, provider, &zone, statusData, params)
	if err != nil || done {
		return result, err
	}

	if selection.ID == "" {
		return r.createHostedZone(ctx, provider, &zone, zoneClass, params)
	}

	adoptionID, adopting, err := route53ZoneAdoptionID(&zone)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, &zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	hostedZone, err := provider.GetHostedZone(ctx, selection.ID)
	if err != nil {
		if adopting && isProviderNotFound(err) {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceNotFound", "adopted Route 53 hosted zone was not found")
		}
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}
	if adopting {
		if hostedZone.ID != adoptionID {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", "adopted Route 53 hosted zone ID does not match spec.adoption.hostedZoneId")
		}
		if message := hostedZoneSpecMismatch(&zone, params, hostedZone); message != "" {
			return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", message)
		}
	} else if message := hostedZoneSpecMismatch(&zone, params, hostedZone); message != "" {
		return ctrl.Result{}, r.setProgrammed(ctx, &zone, metav1.ConditionFalse, "ExternalResourceMismatch", message)
	}

	if err := provider.TagHostedZone(ctx, hostedZone.ID, hostedZoneTags(&zone, zoneClass, params)); err != nil {
		return r.failProgrammedForProviderError(ctx, &zone, err)
	}

	if err := r.patchRoute53ZoneStatus(ctx, &zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.HostedZoneID = hostedZone.ID
		if hostedZone.CallerReference == callerReferenceForZoneState(&zone, statusData) {
			data.CallerReference = hostedZone.CallerReference
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	result = ctrl.Result{}
	result, err = r.refreshHostedZoneStatus(ctx, provider, &zone, hostedZone)
	if err != nil || result.Requeue || result.RequeueAfter > 0 {
		return result, err
	}

	result, err = r.reconcileZoneRecordSets(ctx, provider, &zone, zoneClass, hostedZone)
	if err != nil || result.Requeue || result.RequeueAfter > 0 {
		return result, err
	}

	return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
}

func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Provider == nil && r.ProviderFactory == nil {
		return errors.New("route53 provider is required")
	}
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if r.ControllerName == "" {
		r.ControllerName = DefaultControllerName
	}
	if r.RequeueAfter == 0 {
		r.RequeueAfter = defaultRoute53ZoneRequeueAfter
	}
	if r.ChangeCheckAfter == 0 {
		r.ChangeCheckAfter = defaultRoute53ChangeCheckAfter
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("route53-zoneunit").
		For(&dnsv1alpha1.ZoneUnit{}).
		Watches(&dnsv1alpha1.ZoneClass{}, handler.EnqueueRequestsFromMapFunc(r.mapZoneClassToZoneUnits)).
		Watches(&dnsv1alpha1.Provider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderToZoneUnits)).
		Watches(&route53v1alpha1.Route53Identity{}, handler.EnqueueRequestsFromMapFunc(r.mapIdentityToZoneUnits)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToZoneUnits)).
		Complete(r)
}

func (r *ZoneReconciler) mapZoneClassToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	zoneClass, ok := obj.(*dnsv1alpha1.ZoneClass)
	if !ok {
		return nil
	}
	return r.zoneUnitRequestsForZoneClass(ctx, zoneClass.Namespace, zoneClass.Name)
}

func (r *ZoneReconciler) mapNamespaceToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	namespace, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil
	}
	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, unit := range units.Items {
		if unit.Namespace == namespace.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
			continue
		}
		for _, recordSet := range unit.Spec.RecordSets {
			if recordSet.RecordSetNamespace == namespace.Name {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
				break
			}
		}
	}
	return requests
}

func (r *ZoneReconciler) mapProviderToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	provider, ok := obj.(*dnsv1alpha1.Provider)
	if !ok {
		return nil
	}

	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, unit := range units.Items {
		if unit.Spec.Provider.Name == provider.Name {
			requests = appendUniqueRequests(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
		}
	}
	return requests
}

func (r *ZoneReconciler) mapIdentityToZoneUnits(ctx context.Context, obj client.Object) []reconcile.Request {
	identity, ok := obj.(*route53v1alpha1.Route53Identity)
	if !ok {
		return nil
	}

	var zoneClasses dnsv1alpha1.ZoneClassList
	if err := r.List(ctx, &zoneClasses, client.InNamespace(identity.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, zoneClass := range zoneClasses.Items {
		provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
		if err != nil || zoneClass.Spec.ControllerName != r.controllerName() || !route53ProviderVersionMatches(provider, version, r.providerReference()) {
			continue
		}
		if zoneClass.Spec.IdentityRef.Name == identity.Name {
			requests = appendUniqueRequests(requests, r.zoneUnitRequestsForZoneClass(ctx, zoneClass.Namespace, zoneClass.Name)...)
		}
	}
	return requests
}

func (r *ZoneReconciler) zoneUnitRequestsForZoneClass(ctx context.Context, namespace, name string) []reconcile.Request {
	var units dnsv1alpha1.ZoneUnitList
	if err := r.List(ctx, &units); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, unit := range units.Items {
		if unit.Spec.Zone.ZoneClassRef.Namespace == namespace && unit.Spec.Zone.ZoneClassRef.Name == name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&unit)})
		}
	}
	return requests
}

func appendUniqueRequests(requests []reconcile.Request, newRequests ...reconcile.Request) []reconcile.Request {
	seen := make(map[reconcile.Request]struct{}, len(requests)+len(newRequests))
	for _, request := range requests {
		seen[request] = struct{}{}
	}
	for _, request := range newRequests {
		if _, ok := seen[request]; ok {
			continue
		}
		requests = append(requests, request)
		seen[request] = struct{}{}
	}
	return requests
}

func (r *ZoneReconciler) acceptZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*dnsv1alpha1.ZoneClass, *route53v1alpha1.Route53ZoneClassParameters, *route53v1alpha1.Route53Identity, bool, error) {
	zoneClassNamespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		zoneClassNamespace = *zone.Spec.ZoneClassRef.Namespace
	}

	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClassNamespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found")
		}
		return nil, nil, nil, false, err
	}

	if zoneClass.Spec.ControllerName != r.controllerName() {
		return &zoneClass, nil, nil, false, nil
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
	if err != nil || provider == nil || version == nil || provider.Name != r.providerReference().Name {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Route 53 controller")
	}

	if zone.Spec.Provider != zoneClass.Spec.Provider {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ProviderMismatch", "Zone provider does not match referenced ZoneClass")
	}

	allowed, err := r.zoneAllowedByClass(ctx, &zoneClass, zone.Namespace)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if !allowed {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass")
	}

	if !isAcceptedCurrent(zoneClass.Status.Conditions, zoneClass.Generation) {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted")
	}

	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := route53ZoneClassParameters(converted)
	if err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}

	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	if err != nil || !accepted {
		return nil, nil, nil, false, err
	}
	_, adopting, err := route53ZoneAdoptionID(zone)
	if err != nil {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if !adopting && zoneCreationPolicy(params) == route53v1alpha1.ZoneCreationPolicyDeny {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", "Route 53 parameters deny hosted zone creation")
	}
	if key, ok := reservedTagConflict(params.Tags); ok {
		return nil, nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "DeniedByPolicy", fmt.Sprintf("tag key %q is reserved", key))
	}

	if err := r.setAccepted(ctx, zone, metav1.ConditionTrue, "Accepted", "Zone is accepted by Route 53 policy"); err != nil {
		return nil, nil, nil, false, err
	}
	return &zoneClass, params, identity, true, nil
}

func (r *ZoneReconciler) acceptIdentityRef(ctx context.Context, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass) (*route53v1alpha1.Route53Identity, bool, error) {
	if zoneClass.Spec.IdentityRef.Name == "" {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef.name is required")
	}

	var identity route53v1alpha1.Route53Identity
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClass.Namespace, Name: zoneClass.Spec.IdentityRef.Name}, &identity); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity was not found")
		}
		return nil, false, err
	}

	accepted := meta.FindStatusCondition(identity.Status.Conditions, string(dnsv1alpha1.ConditionAccepted))
	if accepted != nil && accepted.Status == metav1.ConditionFalse && accepted.ObservedGeneration == identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidIdentityRef", "referenced Route53Identity is not accepted")
	}
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != identity.Generation {
		return nil, false, r.setAccepted(ctx, zone, metav1.ConditionUnknown, "IdentityNotResolved", "referenced Route53Identity acceptance is not resolved")
	}

	return &identity, true, nil
}

func (r *ZoneReconciler) providerForIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (Provider, error) {
	if r.ProviderFactory != nil {
		return r.ProviderFactory.ProviderForIdentity(ctx, identity)
	}
	if r.Provider != nil {
		return r.Provider, nil
	}
	return nil, errors.New("route53 provider is required")
}

func (r *ZoneReconciler) reconcileFinalizer(ctx context.Context, unit *dnsv1alpha1.ZoneUnit, params *route53v1alpha1.Route53ZoneClassParameters) (bool, error) {
	hasFinalizer := slices.Contains(unit.Finalizers, ZoneFinalizer)
	shouldHaveFinalizer := zoneDeletionPolicy(params) == route53v1alpha1.ZoneDeletionPolicyDelete

	switch {
	case shouldHaveFinalizer && !hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
		return true, client.IgnoreNotFound(r.Patch(ctx, unit, client.MergeFrom(base)))
	case !shouldHaveFinalizer && hasFinalizer:
		base := unit.DeepCopy()
		unit.Finalizers = slices.DeleteFunc(unit.Finalizers, func(finalizer string) bool {
			return finalizer == ZoneFinalizer
		})
		return true, client.IgnoreNotFound(r.Patch(ctx, unit, client.MergeFrom(base)))
	default:
		return false, nil
	}
}

func (r *ZoneReconciler) reconcileDelete(ctx context.Context, zone *dnsv1alpha1.Zone, unit *dnsv1alpha1.ZoneUnit) (ctrl.Result, error) {
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		return ctrl.Result{}, nil
	}

	params, identity, accepted, err := r.deleteContextForZone(ctx, zone)
	if err != nil || !accepted {
		return ctrl.Result{}, err
	}
	if zoneDeletionPolicy(params) == route53v1alpha1.ZoneDeletionPolicyRetain {
		r.recordEvent(zone, corev1.EventTypeNormal, "ExternalResourceRetained", "Zone was deleted and the external DNS zone was retained by policy")
		return ctrl.Result{}, r.removeFinalizer(ctx, unit)
	}
	if !isReadyCurrent(identity.Status.Conditions, identity.Generation) {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderIdentityNotReady", "referenced Route53Identity is not Ready"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter()}, nil
	}
	provider, err := r.providerForIdentity(ctx, identity)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}

	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	if result, done, err := r.refreshDeletingHostedZoneChange(ctx, provider, zone, statusData); done || err != nil {
		return result, err
	}

	hostedZoneID, err := hostedZoneIDForDelete(zone, statusData)
	if err != nil {
		return ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	}
	if hostedZoneID != "" {
		if _, err := provider.GetHostedZone(ctx, hostedZoneID); err != nil {
			if isProviderNotFound(err) {
				return ctrl.Result{}, r.removeFinalizer(ctx, unit)
			}
			return r.failProgrammedForProviderError(ctx, zone, err)
		}

		change, err := provider.DeleteHostedZone(ctx, hostedZoneID)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, zone, err)
		}
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeSubmitted", fmt.Sprintf("Route 53 hosted zone delete change was submitted for %s", hostedZoneID))
		if change != nil {
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				data.PendingHostedZoneChange = pendingChangeFromChange(change, "DELETE")
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		if change != nil && change.Status == route53v1alpha1.Route53ChangeStatusPending {
			if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone deletion is pending"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
		}
	}

	r.recordEvent(zone, corev1.EventTypeNormal, "ExternalResourceDeleted", "Zone was deleted and the external DNS zone was deleted")
	return ctrl.Result{}, r.removeFinalizer(ctx, unit)
}

func (r *ZoneReconciler) refreshDeletingHostedZoneChange(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (ctrl.Result, bool, error) {
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange == nil || pendingChange.ID == "" || pendingChange.Status != route53v1alpha1.Route53ChangeStatusPending || pendingChange.Operation != "DELETE" {
		return ctrl.Result{}, false, nil
	}

	change, err := provider.GetChange(ctx, pendingChange.ID)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if change != nil {
		pendingChange = pendingChangeFromChange(change, "DELETE")
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = pendingChange
		}); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone deletion is pending"); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone delete change %s is INSYNC", pendingChange.ID))
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingHostedZoneChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}

func (r *ZoneReconciler) deleteContextForZone(ctx context.Context, zone *dnsv1alpha1.Zone) (*route53v1alpha1.Route53ZoneClassParameters, *route53v1alpha1.Route53Identity, bool, error) {
	zoneClassNamespace := zone.Namespace
	if zone.Spec.ZoneClassRef.Namespace != nil && *zone.Spec.ZoneClassRef.Namespace != "" {
		zoneClassNamespace = *zone.Spec.ZoneClassRef.Namespace
	}

	var zoneClass dnsv1alpha1.ZoneClass
	if err := r.Get(ctx, client.ObjectKey{Namespace: zoneClassNamespace, Name: zone.Spec.ZoneClassRef.Name}, &zoneClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found")
		}
		return nil, nil, false, err
	}

	if zoneClass.Spec.ControllerName != r.controllerName() {
		return nil, nil, false, nil
	}
	provider, version, err := resolveDNSProviderVersion(ctx, r.Client, zoneClass.Spec.Provider)
	if err != nil || provider == nil || version == nil || provider.Name != r.providerReference().Name {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", "Zone provider is not handled by Route 53 controller")
	}
	if zone.Spec.Provider != zoneClass.Spec.Provider {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "ProviderMismatch", "Zone provider does not match referenced ZoneClass")
	}
	parameters, payloadErr := providercontract.ConvertZoneClassParametersToStorage(provider, version, &zoneClass)
	if payloadErr != nil {
		acceptance := providercontract.PayloadErrorToAcceptance(payloadErr)
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionStatus(acceptance.Status), acceptance.Reason, acceptance.Message)
	}
	converted := zoneClass.DeepCopy()
	converted.Spec.Parameters = parameters
	params, err := route53ZoneClassParameters(converted)
	if err != nil {
		return nil, nil, false, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidProvider", err.Error())
	}

	identity, accepted, err := r.acceptIdentityRef(ctx, zone, &zoneClass)
	return params, identity, accepted, err
}

func (r *ZoneReconciler) selectHostedZone(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData, params *route53v1alpha1.Route53ZoneClassParameters) (HostedZone, bool, ctrl.Result, error) {
	if adoptionID, adopting, err := route53ZoneAdoptionID(zone); err != nil {
		return HostedZone{}, true, ctrl.Result{}, r.setAccepted(ctx, zone, metav1.ConditionFalse, "InvalidAdoption", err.Error())
	} else if adopting {
		return HostedZone{ID: adoptionID}, false, ctrl.Result{}, nil
	}

	statusID := normalizeHostedZoneID(statusData.HostedZoneID)
	if statusID != "" {
		if hostedZone, err := provider.GetHostedZone(ctx, statusID); err != nil {
			if isProviderNotFound(err) {
				if err := r.clearHostedZoneProviderStatus(ctx, zone); err != nil {
					return HostedZone{}, true, ctrl.Result{}, err
				}
			} else {
				result, err := r.failProgrammedForProviderError(ctx, zone, err)
				return HostedZone{}, true, result, err
			}
		} else {
			return hostedZone, false, ctrl.Result{}, nil
		}
	}

	sameNameZones, err := provider.ListHostedZonesByName(ctx, zone.Spec.DomainName)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return HostedZone{}, true, result, err
	}

	callerReference := callerReferenceForZoneState(zone, statusData)
	var selected HostedZone
	for _, hostedZone := range sameNameZones {
		if hostedZone.Private {
			continue
		}
		if hostedZone.CallerReference == callerReference {
			selected = hostedZone
			break
		}
	}

	if params != nil && sameNameZonePolicy(params) == route53v1alpha1.SameNameZonePolicyDeny {
		for _, hostedZone := range sameNameZones {
			if hostedZone.Private {
				continue
			}
			if selected.ID != "" && sameHostedZoneID(hostedZone.ID, selected.ID) {
				continue
			}
			err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderConflict", "same-name Route 53 hosted zone already exists")
			return HostedZone{}, true, ctrl.Result{}, err
		}
	}

	return selected, false, ctrl.Result{}, nil
}

func (r *ZoneReconciler) clearHostedZoneProviderStatus(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return err
	}
	statusData.HostedZoneID = ""
	statusData.CallerReference = ""
	statusData.PendingHostedZoneChange = nil
	statusData.PendingRecordSetChange = nil
	raw, err := json.Marshal(statusData)
	if err != nil {
		return err
	}
	return r.patchZoneStatus(ctx, zone, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = nil
		status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: raw}}
	})
}

func (r *ZoneReconciler) createHostedZone(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, zoneClass *dnsv1alpha1.ZoneClass, params *route53v1alpha1.Route53ZoneClassParameters) (ctrl.Result, error) {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, err
	}
	callerReference := callerReferenceForZoneState(zone, statusData)
	if statusData.CallerReference == "" {
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.CallerReference = callerReference
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	created, err := provider.CreateHostedZone(ctx, zone.Spec.DomainName, callerReference)
	if err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}
	if created.Change != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeSubmitted", fmt.Sprintf("Route 53 hosted zone create change was submitted for %s", created.HostedZone.ID))
	}

	if err := provider.TagHostedZone(ctx, created.HostedZone.ID, hostedZoneTags(zone, zoneClass, params)); err != nil {
		return r.failProgrammedForProviderError(ctx, zone, err)
	}

	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.HostedZoneID = created.HostedZone.ID
		data.CallerReference = callerReference
		if created.Change != nil && created.Change.Status == route53v1alpha1.Route53ChangeStatusPending {
			data.PendingHostedZoneChange = pendingChangeFromChange(created.Change, "CREATE")
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	return r.refreshHostedZoneStatus(ctx, provider, zone, created.HostedZone)
}

func (r *ZoneReconciler) refreshHostedZoneStatus(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, hostedZone HostedZone) (ctrl.Result, error) {
	statusData, err := route53ZoneStatusData(zone)
	if err != nil {
		return ctrl.Result{}, r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ReconcileError", err.Error())
	}
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange != nil && pendingChange.ID != "" && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		change, err := provider.GetChange(ctx, pendingChange.ID)
		if err != nil {
			return r.failProgrammedForProviderError(ctx, zone, err)
		}
		if change != nil {
			pendingChange = pendingChangeFromChange(change, pendingChange.Operation)
			if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
				data.PendingHostedZoneChange = pendingChange
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	if err := r.patchZoneStatus(ctx, zone, func(status *dnsv1alpha1.ZoneStatus) {
		status.NameServers = slices.Clone(hostedZone.NameServers)
		if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
			setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionFalse, "ProviderChangePending", "Route 53 change is pending", zone.Generation)
			return
		}
		setCondition(&status.Conditions, string(dnsv1alpha1.ConditionProgrammed), metav1.ConditionTrue, "Programmed", "Route 53 hosted zone is programmed", zone.Generation)
	}); err != nil {
		return ctrl.Result{}, err
	}

	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone change %s is INSYNC", pendingChange.ID))
		return ctrl.Result{Requeue: true}, r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = nil
		})
	}
	return ctrl.Result{}, nil
}

func (r *ZoneReconciler) refreshPendingHostedZoneChange(ctx context.Context, provider Provider, zone *dnsv1alpha1.Zone, statusData route53v1alpha1.Route53ZoneStatusData) (ctrl.Result, bool, error) {
	pendingChange := statusData.PendingHostedZoneChange
	if pendingChange == nil || pendingChange.ID == "" || pendingChange.Status != route53v1alpha1.Route53ChangeStatusPending {
		return ctrl.Result{}, false, nil
	}

	change, err := provider.GetChange(ctx, pendingChange.ID)
	if err != nil {
		result, err := r.failProgrammedForProviderError(ctx, zone, err)
		return result, true, err
	}
	if change != nil {
		pendingChange = pendingChangeFromChange(change, pendingChange.Operation)
		if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
			data.PendingHostedZoneChange = pendingChange
		}); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	if pendingChange != nil && pendingChange.Status == route53v1alpha1.Route53ChangeStatusPending {
		if err := r.setProgrammed(ctx, zone, metav1.ConditionFalse, "ProviderChangePending", "Route 53 hosted zone change is pending"); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{RequeueAfter: r.changeCheckAfter()}, true, nil
	}
	if pendingChange != nil {
		r.recordEvent(zone, corev1.EventTypeNormal, "Route53HostedZoneChangeInSync", fmt.Sprintf("Route 53 hosted zone change %s is INSYNC", pendingChange.ID))
	}
	if err := r.patchRoute53ZoneStatus(ctx, zone, func(data *route53v1alpha1.Route53ZoneStatusData) {
		data.PendingHostedZoneChange = nil
	}); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{Requeue: true}, true, nil
}

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
	base := zone.DeepCopy()
	mutate(&zone.Status)
	if equality.Semantic.DeepEqual(base.Status, zone.Status) {
		return nil
	}

	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	unitBase := unit.DeepCopy()
	if unit.Status.Zone == nil {
		unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{}
	}
	unit.Status.Zone.NameServers = slices.Clone(zone.Status.NameServers)
	unit.Status.Zone.Provider = nil
	if zone.Status.Provider != nil && len(zone.Status.Provider.Data.Raw) > 0 {
		unit.Status.Zone.Provider = &dnsv1alpha1.ProviderStatus{Data: zone.Status.Provider.Data}
	}
	if zone.Status.Provider != nil && len(zone.Status.Provider.State.Raw) > 0 {
		if unit.Status.Provider == nil {
			unit.Status.Provider = &dnsv1alpha1.ProviderStatus{}
		}
		unit.Status.Provider.State = zone.Status.Provider.State
	} else if unit.Status.Provider != nil {
		unit.Status.Provider.State = runtime.RawExtension{}
		if len(unit.Status.Provider.Data.Raw) == 0 {
			unit.Status.Provider = nil
		}
	}
	unit.Status.Zone.Conditions = slices.Clone(zone.Status.Conditions)
	unit.Status.ObservedGeneration = unit.Generation
	setZoneUnitProgrammedCondition(&unit)
	if equality.Semantic.DeepEqual(unitBase.Status, unit.Status) {
		return nil
	}
	return client.IgnoreNotFound(r.Status().Patch(ctx, &unit, client.MergeFrom(unitBase)))
}

func (r *ZoneReconciler) hydrateRoute53ZoneStatusFromZoneUnit(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, client.ObjectKey{Namespace: zone.Namespace, Name: zone.Name}, &unit); err != nil {
		return client.IgnoreNotFound(err)
	}
	applyRoute53ZoneStatusFromZoneUnit(zone, &unit)
	return nil
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
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("Zone status.provider.state must match Route 53 schema: %w", err)
		}
		return data, nil
	}
	if zone.Status.Provider != nil && len(zone.Status.Provider.Data.Raw) > 0 {
		var data route53v1alpha1.Route53ZoneStatusData
		if err := json.Unmarshal(zone.Status.Provider.Data.Raw, &data); err != nil {
			return route53v1alpha1.Route53ZoneStatusData{}, fmt.Errorf("Zone status.provider.data must match Route 53 schema: %w", err)
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
