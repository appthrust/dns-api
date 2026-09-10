package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 AddToScheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("dns AddToScheme: %v", err)
	}
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("route53 AddToScheme: %v", err)
	}
	return scheme
}
func sameNamespaceClassObjects(namespace string, tags map[string]string) []client.Object {
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		route53Provider(),
		route53ZoneClass(namespace, "route53-public", tags),
		acceptedRoute53Identity(namespace, "route53-dev"),
		route53ZoneUnit(namespace, "apps-example-com", namespace, "route53-public"),
		&dnsv1alpha1.Zone{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  namespace,
				Name:       "apps-example-com",
				UID:        types.UID("11111111-2222-3333-4444-555555555555"),
				Generation: 1,
			},
			Spec: dnsv1alpha1.ZoneSpec{
				DomainName:   "apps.example.com",
				ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53-public"},
				Provider:     route53v1alpha1.ProviderRef,
			},
		},
	}
}
func route53ZoneClass(namespace, name string, tags map[string]string) *dnsv1alpha1.ZoneClass {
	parameters := route53v1alpha1.Route53ZoneClassParameters{
		Tags: tags,
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		panic(err)
	}
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Provider:       route53v1alpha1.ProviderRef,
			ControllerName: DefaultControllerName,
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "route53-dev"},
			Parameters:     runtime.RawExtension{Raw: raw},
			AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
				Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
			},
		},
		Status: dnsv1alpha1.ZoneClassStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
			},
		},
	}
}
func route53Provider() *dnsv1alpha1.Provider {
	return &dnsv1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: route53v1alpha1.ProviderName},
		Spec: dnsv1alpha1.ProviderSpec{
			Display: dnsv1alpha1.ProviderDisplay{Name: "Amazon Route 53"},
			Versions: []dnsv1alpha1.ProviderVersion{
				{
					Name:    route53v1alpha1.ProviderVersion,
					Served:  true,
					Storage: true,
					Identity: &dnsv1alpha1.ProviderIdentity{
						Resource: dnsv1alpha1.ProviderIdentityResource{
							Group: "route53.dns.appthrust.io",
							Kind:  "Route53Identity",
							Scope: "Namespaced",
						},
					},
					RecordSet: dnsv1alpha1.ProviderRecordSet{
						SupportedTypes: []dnsv1alpha1.RecordType{
							dnsv1alpha1.RecordTypeA,
							dnsv1alpha1.RecordTypeAAAA,
							dnsv1alpha1.RecordTypeTXT,
							dnsv1alpha1.RecordTypeCNAME,
							dnsv1alpha1.RecordTypeMX,
							dnsv1alpha1.RecordTypeCAA,
							dnsv1alpha1.RecordTypeNS,
						},
					},
				},
			},
		},
	}
}
func route53RecordSetObjects(t *testing.T, recordSet *dnsv1alpha1.RecordSet) []client.Object {
	t.Helper()
	zone := route53ReadyZone(t, "app", "apps-example-com")
	unit := route53ZoneUnit("app", "apps-example-com", "app", "route53-public")
	unit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{route53ZoneUnitRecordSetSpec(recordSet)}
	if recordSet.Status.Provider != nil || len(recordSet.Status.Conditions) > 0 {
		unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
			{
				RecordSetNamespace: recordSet.Namespace,
				RecordSetName:      recordSet.Name,
				RecordSetUID:       recordSet.UID,
				ObservedGeneration: recordSet.Status.ObservedGeneration,
				Provider:           recordSet.Status.Provider,
				Conditions:         slices.Clone(recordSet.Status.Conditions),
			},
		}
	}
	return []client.Object{
		route53Provider(),
		route53ZoneClass("app", "route53-public", nil),
		acceptedRoute53Identity("app", "route53-dev"),
		zone,
		unit,
		recordSet,
	}
}
func route53ZoneUnit(zoneNamespace, zoneName, zoneClassNamespace, zoneClassName string) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: zoneNamespace,
			Name:      zoneName,
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: route53v1alpha1.ProviderName, Version: route53v1alpha1.ProviderVersion},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: zoneName},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: zoneClassNamespace, Name: zoneClassName},
			},
		},
	}
}
func markRoute53ZoneUnitDeleting(unit *dnsv1alpha1.ZoneUnit, deletionTime metav1.Time) {
	unit.DeletionTimestamp = &deletionTime
	if !slices.Contains(unit.Finalizers, ZoneFinalizer) {
		unit.Finalizers = append(unit.Finalizers, ZoneFinalizer)
	}
}
func route53ZoneUnitWithRecordSetItem(zoneNamespace, zoneName, recordSetNamespace, recordSetName, recordName string, recordType dnsv1alpha1.RecordType) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: zoneNamespace,
			Name:      zoneName,
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: dnsv1alpha1.ProviderReference{Name: route53v1alpha1.ProviderName, Version: route53v1alpha1.ProviderVersion},
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: zoneName},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: "route53-public"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				{
					RecordSetNamespace: recordSetNamespace,
					RecordSetName:      recordSetName,
					RecordSetUID:       route53TestRecordSetUID(recordSetNamespace, recordSetName),
					Name:               recordName,
					Type:               recordType,
				},
			},
		},
	}
}
func route53ZoneUnitRecordSetSpec(recordSet *dnsv1alpha1.RecordSet) dnsv1alpha1.ZoneUnitRecordSetSpec {
	return dnsv1alpha1.ZoneUnitRecordSetSpec{
		RecordSetNamespace: recordSet.Namespace,
		RecordSetName:      recordSet.Name,
		RecordSetUID:       recordSet.UID,
		ObservedGeneration: recordSet.Generation,
		Name:               recordSet.Spec.Name,
		Type:               recordSet.Spec.Type,
		TTL:                recordSet.Spec.TTL,
		A:                  recordSet.Spec.A,
		AAAA:               recordSet.Spec.AAAA,
		TXT:                recordSet.Spec.TXT,
		CNAME:              recordSet.Spec.CNAME,
		MX:                 recordSet.Spec.MX,
		CAA:                recordSet.Spec.CAA,
		NS:                 recordSet.Spec.NS,
		Options:            recordSet.Spec.Options,
		Adoption:           recordSet.Spec.Adoption,
		DeletionRequested:  !recordSet.DeletionTimestamp.IsZero(),
	}
}
func route53ReadyZone(t *testing.T, namespace, name string) *dnsv1alpha1.Zone {
	t.Helper()
	zone := &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  namespace,
			Name:       name,
			UID:        types.UID("11111111-2222-3333-4444-555555555555"),
			Generation: 1,
		},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName:   "apps.example.com",
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53-public"},
			Provider:     route53v1alpha1.ProviderRef,
		},
		Status: dnsv1alpha1.ZoneStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
				{
					Type:               string(dnsv1alpha1.ConditionProgrammed),
					Status:             metav1.ConditionTrue,
					Reason:             "Programmed",
					ObservedGeneration: 1,
				},
			},
		},
	}
	setZoneStatusData(t, zone, route53v1alpha1.Route53ZoneStatusData{HostedZoneID: "Z000001"})
	return zone
}
func route53TestRecordSetUID(namespace, name string) types.UID {
	return types.UID("test-route53-recordset-" + namespace + "-" + name)
}

func route53ARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     "www",
			TTL:      ptr(int32(300)),
			A: &dnsv1alpha1.ARecordSet{
				Addresses: []string{"192.0.2.10"},
			},
		},
	}
}
func route53AAAARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeAAAA,
			Name:     "www",
			TTL:      ptr(int32(300)),
			AAAA: &dnsv1alpha1.AAAARecordSet{
				Addresses: []string{"2001:db8::10"},
			},
		},
	}
}
func route53TXTRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeTXT,
			Name:     "_acme-challenge",
			TTL:      ptr(int32(300)),
			TXT: &dnsv1alpha1.TXTRecordSet{
				Values: []string{"challenge-token", "v=spf1 include:_spf.example.net ~all"},
			},
		},
	}
}
func route53CNAMERecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeCNAME,
			Name:     "www",
			TTL:      ptr(int32(300)),
			CNAME: &dnsv1alpha1.CNAMERecordSet{
				Target: "target.example.net",
			},
		},
	}
}
func route53MXRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeMX,
			Name:     "@",
			TTL:      ptr(int32(300)),
			MX: &dnsv1alpha1.MXRecordSet{
				Records: []dnsv1alpha1.MXRecord{
					{Preference: 10, Exchange: "mail1.example.net"},
					{Preference: 20, Exchange: "mail2.example.net"},
				},
			},
		},
	}
}
func route53CAARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeCAA,
			Name:     "@",
			TTL:      ptr(int32(300)),
			CAA: &dnsv1alpha1.CAARecordSet{
				Records: []dnsv1alpha1.CAARecord{
					{Flags: 0, Tag: "issue", Value: "letsencrypt.org"},
					{Flags: 0, Tag: "iodef", Value: "mailto:security@example.com"},
				},
			},
		},
	}
}
func route53NSRecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeNS,
			Name:     "delegated",
			TTL:      ptr(int32(300)),
			NS: &dnsv1alpha1.NSRecordSet{
				NameServers: []string{"ns-111.example-dns.net", "ns-222.example-dns.net"},
			},
		},
	}
}
func route53AliasARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: route53TestRecordSetUID(namespace, name), Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     "alias",
			Options:  runtime.RawExtension{Raw: []byte(`{"alias":{"dnsName":"target.apps.example.com.","hostedZoneID":"Z000001","evaluateTargetHealth":false}}`)},
		},
	}
}
func deletingRoute53ARecordSet(t *testing.T) *dnsv1alpha1.RecordSet {
	t.Helper()
	recordSet := route53ARecordSet("app", "www")
	deletionTime := metav1.Now()
	recordSet.DeletionTimestamp = &deletionTime
	recordSet.Finalizers = []string{RecordSetFinalizer}
	return recordSet
}
func setZoneStatusData(t *testing.T, zone *dnsv1alpha1.Zone, data route53v1alpha1.Route53ZoneStatusData) {
	t.Helper()
	stateRaw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	publicData := route53v1alpha1.Route53ZoneStatusData{}
	if data.HostedZoneID != "" {
		publicData.HostedZoneID = data.HostedZoneID
	}
	publicRaw, err := json.Marshal(publicData)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	zone.Status.Provider = &dnsv1alpha1.ProviderStatus{
		Data:  runtime.RawExtension{Raw: publicRaw},
		State: runtime.RawExtension{Raw: stateRaw},
	}
}
func setRecordSetStatusData(t *testing.T, recordSet *dnsv1alpha1.RecordSet, data route53v1alpha1.Route53RecordSetStatusData) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}
}
func mustZoneUnitRecordSetStatusState(t *testing.T, ctx context.Context, k8sClient client.Client, zoneNamespace, zoneName, recordSetNamespace, recordSetName string) route53v1alpha1.Route53RecordSetStatusData {
	t.Helper()
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: zoneNamespace, Name: zoneName}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	index := slices.IndexFunc(unit.Status.RecordSets, func(status dnsv1alpha1.ZoneUnitRecordSetStatus) bool {
		return status.RecordSetNamespace == recordSetNamespace && status.RecordSetName == recordSetName
	})
	if index < 0 {
		t.Fatalf("ZoneUnit status.recordSets has no entry for %s/%s: %#v", recordSetNamespace, recordSetName, unit.Status.RecordSets)
	}
	provider := unit.Status.RecordSets[index].Provider
	if provider == nil || len(provider.State.Raw) == 0 {
		t.Fatalf("ZoneUnit status.recordSets[%d].provider.state is empty", index)
	}
	var data route53v1alpha1.Route53RecordSetStatusData
	if err := json.Unmarshal(provider.State.Raw, &data); err != nil {
		t.Fatalf("ZoneUnit status.recordSets[%d].provider.state must match Route 53 schema: %v", index, err)
	}
	return data
}
func acceptedRoute53Identity(namespace, name string) *route53v1alpha1.Route53Identity {
	return &route53v1alpha1.Route53Identity{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec: route53v1alpha1.Route53IdentitySpec{
			AccountID: "123456789012",
			Region:    "ap-northeast-1",
			Credentials: route53v1alpha1.Route53Credentials{
				Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
			},
		},
		Status: route53v1alpha1.Route53IdentityStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(dnsv1alpha1.ConditionAccepted),
					Status:             metav1.ConditionTrue,
					Reason:             "Accepted",
					ObservedGeneration: 1,
				},
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Ready",
					ObservedGeneration: 1,
				},
			},
		},
	}
}
func assertCondition(t *testing.T, conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(conditions, conditionType)
	if condition == nil {
		t.Fatalf("condition %s was not found in %#v", conditionType, conditions)
	}
	if condition.Status != status || condition.Reason != reason {
		t.Fatalf("condition %s = (%s, %s), want (%s, %s)", conditionType, condition.Status, condition.Reason, status, reason)
	}
	return *condition
}
func mustZoneStatusData(t *testing.T, zone *dnsv1alpha1.Zone) route53v1alpha1.Route53ZoneStatusData {
	t.Helper()
	data, err := route53ZoneStatusData(zone)
	if err != nil {
		t.Fatalf("route53ZoneStatusData returned error: %v", err)
	}
	return data
}
func mustZoneUnitStatusData(t *testing.T, ctx context.Context, k8sClient client.Client, namespace, name string) route53v1alpha1.Route53ZoneStatusData {
	t.Helper()
	var unit dnsv1alpha1.ZoneUnit
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &unit); err != nil {
		t.Fatalf("ZoneUnit was not found: %v", err)
	}
	data, err := route53ZoneUnitStatusData(&unit)
	if err != nil {
		t.Fatalf("route53ZoneUnitStatusData returned error: %v", err)
	}
	return data
}
func ptr[T any](value T) *T {
	return &value
}

type recordedEvent struct {
	Object string
	Reason string
}
type capturingRecorder struct {
	events []recordedEvent
}

func (r *capturingRecorder) Event(object runtime.Object, _ string, reason, _ string) {
	r.events = append(r.events, recordedEvent{Object: eventObjectName(object), Reason: reason})
}
func (r *capturingRecorder) Eventf(object runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}
func (r *capturingRecorder) AnnotatedEventf(object runtime.Object, _ map[string]string, eventType, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventType, reason, fmt.Sprintf(messageFmt, args...))
}
func (r *capturingRecorder) has(object, reason string) bool {
	for _, event := range r.events {
		if event.Object == object && event.Reason == reason {
			return true
		}
	}
	return false
}
func eventObjectName(object runtime.Object) string {
	switch object.(type) {
	case *dnsv1alpha1.RecordSet:
		return "RecordSet"
	case *dnsv1alpha1.Zone:
		return "Zone"
	case *route53v1alpha1.Route53Identity:
		return "Route53Identity"
	default:
		return fmt.Sprintf("%T", object)
	}
}

type createHostedZoneRequest struct {
	domainName      string
	callerReference string
}
type fakeProvider struct {
	zones      map[string]HostedZone
	tags       map[string]map[string]string
	changes    map[string]*route53v1alpha1.Route53Change
	records    map[string]RecordSetResource
	created    []createHostedZoneRequest
	upserted   []RecordSetResource
	deletedRRs []RecordSetResource

	changeRecordSetsErr func(hostedZoneID string, changes []RecordSetChange) error
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		zones:   map[string]HostedZone{},
		tags:    map[string]map[string]string{},
		changes: map[string]*route53v1alpha1.Route53Change{},
		records: map[string]RecordSetResource{},
	}
}
func (p *fakeProvider) GetHostedZone(_ context.Context, id string) (HostedZone, error) {
	zone, ok := p.zones[normalizeHostedZoneID(id)]
	if !ok {
		return HostedZone{}, &smithy.GenericAPIError{
			Code:    "NoSuchHostedZone",
			Message: fmt.Sprintf("hosted zone %s was not found", id),
		}
	}
	return zone, nil
}
func (p *fakeProvider) ListHostedZonesByName(_ context.Context, domainName string) ([]HostedZone, error) {
	var zones []HostedZone
	for _, zone := range p.zones {
		if normalizeDomainName(zone.Name) == domainName {
			zones = append(zones, zone)
		}
	}
	return zones, nil
}
func (p *fakeProvider) CreateHostedZone(_ context.Context, domainName, callerReference string) (CreatedHostedZone, error) {
	p.created = append(p.created, createHostedZoneRequest{domainName: domainName, callerReference: callerReference})
	id := fmt.Sprintf("Z%06d", len(p.created))
	changeID := fmt.Sprintf("C%06d", len(p.created))
	hostedZone := HostedZone{
		ID:              id,
		Name:            domainName,
		CallerReference: callerReference,
		NameServers:     []string{"ns-1.awsdns.example", "ns-2.awsdns.example"},
	}
	p.zones[id] = hostedZone
	p.changes[changeID] = &route53v1alpha1.Route53Change{
		ID:     changeID,
		Status: route53v1alpha1.Route53ChangeStatusPending,
	}
	return CreatedHostedZone{
		HostedZone: hostedZone,
		Change:     p.changes[changeID],
	}, nil
}
func (p *fakeProvider) GetChange(_ context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	change, ok := p.changes[id]
	if !ok {
		return nil, fmt.Errorf("change %s was not found", id)
	}
	return change, nil
}
func (p *fakeProvider) DeleteHostedZone(_ context.Context, id string) (*route53v1alpha1.Route53Change, error) {
	delete(p.zones, normalizeHostedZoneID(id))
	change := &route53v1alpha1.Route53Change{
		ID:     "CDELETE",
		Status: route53v1alpha1.Route53ChangeStatusPending,
	}
	p.changes[change.ID] = change
	return change, nil
}
func (p *fakeProvider) TagHostedZone(_ context.Context, id string, tags map[string]string) error {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	p.tags[normalizeHostedZoneID(id)] = copied
	return nil
}
func (p *fakeProvider) GetRecordSet(_ context.Context, hostedZoneID, recordName string, recordType dnsv1alpha1.RecordType) (*RecordSetResource, error) {
	record, ok := p.records[recordKey(hostedZoneID, recordName, recordType)]
	if !ok {
		return nil, nil
	}
	copied := copyRecordSetResource(record)
	return &copied, nil
}
func (p *fakeProvider) ListRecordSets(_ context.Context, hostedZoneID string) ([]RecordSetResource, error) {
	var records []RecordSetResource
	for _, record := range p.records {
		if normalizeHostedZoneID(record.HostedZoneID) != normalizeHostedZoneID(hostedZoneID) {
			continue
		}
		records = append(records, copyRecordSetResource(record))
	}
	return records, nil
}
func (p *fakeProvider) ChangeRecordSets(_ context.Context, hostedZoneID string, changes []RecordSetChange) (*route53v1alpha1.Route53Change, error) {
	if p.changeRecordSetsErr != nil {
		if err := p.changeRecordSetsErr(hostedZoneID, changes); err != nil {
			return nil, err
		}
	}
	for _, change := range changes {
		recordSet := copyRecordSetResource(change.RecordSet)
		recordSet.HostedZoneID = normalizeHostedZoneID(hostedZoneID)
		switch change.Action {
		case RecordSetChangeActionDelete:
			p.deletedRRs = append(p.deletedRRs, recordSet)
			delete(p.records, recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type))
		default:
			p.upserted = append(p.upserted, recordSet)
			p.records[recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type)] = recordSet
		}
	}
	changeID := fmt.Sprintf("CRR%06d", len(p.upserted)+len(p.deletedRRs))
	p.changes[changeID] = &route53v1alpha1.Route53Change{ID: changeID, Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes[changeID], nil
}
func (p *fakeProvider) UpsertRecordSet(_ context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	copied := copyRecordSetResource(recordSet)
	p.upserted = append(p.upserted, copied)
	p.records[recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type)] = copied
	changeID := fmt.Sprintf("CRR%06d", len(p.upserted))
	p.changes[changeID] = &route53v1alpha1.Route53Change{ID: changeID, Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes[changeID], nil
}
func (p *fakeProvider) DeleteRecordSet(_ context.Context, recordSet RecordSetResource) (*route53v1alpha1.Route53Change, error) {
	copied := copyRecordSetResource(recordSet)
	p.deletedRRs = append(p.deletedRRs, copied)
	delete(p.records, recordKey(recordSet.HostedZoneID, recordSet.Name, recordSet.Type))
	p.changes["CRRDELETE"] = &route53v1alpha1.Route53Change{ID: "CRRDELETE", Status: route53v1alpha1.Route53ChangeStatusPending}
	return p.changes["CRRDELETE"], nil
}
func recordKey(hostedZoneID, recordName string, recordType dnsv1alpha1.RecordType) string {
	return normalizeHostedZoneID(hostedZoneID) + "|" + normalizeRoute53RecordName(recordName) + "|" + string(recordType)
}
func copyRecordSetResource(recordSet RecordSetResource) RecordSetResource {
	copied := recordSet
	copied.Values = append([]string(nil), recordSet.Values...)
	if recordSet.TTL != nil {
		ttl := *recordSet.TTL
		copied.TTL = &ttl
	}
	if recordSet.Alias != nil {
		alias := *recordSet.Alias
		copied.Alias = &alias
	}
	return copied
}
