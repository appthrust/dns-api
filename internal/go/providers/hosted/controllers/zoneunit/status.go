package zoneunit

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func setCondition(conditions *[]metav1.Condition, kind string, status metav1.ConditionStatus, reason, message string, generation int64, now time.Time) {
	meta.SetStatusCondition(conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: boundedMessage(message), ObservedGeneration: generation, LastTransitionTime: metav1.NewTime(now)})
}
func boundedMessage(message string) string {
	if len(message) <= 2048 {
		return message
	}
	message = message[:2048]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
func providerData(value any) *dnsv1alpha1.ProviderStatus {
	raw, _ := json.Marshal(value)
	return &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}
}
func timePointer(now time.Time) *metav1.Time { value := metav1.NewTime(now); return &value }

type recordReceipt struct {
	Fingerprint   string       `json:"fingerprint"`
	FirstSerial   uint32       `json:"firstSerial"`
	ContentDigest string       `json:"contentDigest"`
	ServedAt      *metav1.Time `json:"servedAt,omitempty"`
}
type serverData struct {
	Endpoint      string      `json:"endpoint"`
	Serial        uint32      `json:"serial"`
	ContentDigest string      `json:"contentDigest"`
	ObservedAt    metav1.Time `json:"observedAt"`
}
type zoneData struct {
	Serial        uint32          `json:"serial"`
	ContentDigest string          `json:"contentDigest"`
	Servers       []serverData    `json:"servers"`
	Delegation    *delegationData `json:"delegation,omitempty"`
}

func setResults(unit *dnsv1alpha1.ZoneUnit, spec hostedv1alpha1.HostedZoneSpec, servers []hostedv1alpha1.HostedZoneServerStatus, served bool, now time.Time) {
	zone := unit.Status.Zone
	var data zoneData
	if zone.Provider != nil {
		_ = json.Unmarshal(zone.Provider.Data.Raw, &data)
	}
	previousData := data
	data.Serial, data.ContentDigest = spec.Serial, spec.ContentDigest
	servers = slices.Clone(servers)
	slices.SortFunc(servers, func(a, b hostedv1alpha1.HostedZoneServerStatus) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	data.Servers = make([]serverData, 0, min(64, len(servers)))
	for _, server := range servers[:min(64, len(servers))] {
		data.Servers = append(data.Servers, serverData{Endpoint: server.Endpoint, Serial: server.Serial, ContentDigest: server.ContentDigest, ObservedAt: server.ObservedAt})
	}
	if zone.Provider == nil || !equality.Semantic.DeepEqual(previousData, data) {
		zone.Provider = providerData(data)
	}
	zone.NameServers = make([]string, len(spec.NameServers))
	for i, server := range spec.NameServers {
		zone.NameServers[i] = server.Name
	}
	setCondition(&zone.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "Hosted DNS Zone binding is accepted.", unit.Spec.Zone.ObservedGeneration, now)
	status, reason, message := metav1.ConditionFalse, hostedv1alpha1.ReasonServingPending, pendingMessage(spec.Serial)
	if served {
		status, reason, message = metav1.ConditionTrue, "Programmed", "Hosted snapshot is served by every fresh hosted server."
	}
	setCondition(&zone.Conditions, "Programmed", status, reason, message, unit.Spec.Zone.ObservedGeneration, now)
	prepareRecordReceipts(unit)
	for i, item := range unit.Spec.RecordSets {
		result := unit.Status.RecordSets[i]
		var receipt recordReceipt
		if result.Provider != nil {
			_ = json.Unmarshal(result.Provider.State.Raw, &receipt)
		}
		previousReceipt := receipt
		fingerprint := dnsprobe.Fingerprint(item)
		if receipt.Fingerprint != fingerprint {
			receipt = recordReceipt{Fingerprint: fingerprint, FirstSerial: spec.Serial, ContentDigest: spec.ContentDigest}
		}
		// Every fresh server must have reached a revision that contains this
		// generation (or omits its cleanup item), not just the fastest replica.
		itemServed := revisionServed(receipt.FirstSerial, receipt.ContentDigest, spec, servers)
		if itemServed && receipt.ServedAt == nil {
			receipt.ServedAt = timePointer(now)
		}
		result.DeletionCompleted = item.DeletionRequested && item.RecordSetUID != "" && itemServed
		priorCondition := meta.FindStatusCondition(result.Conditions, "Programmed")
		priorServed := priorCondition != nil && priorCondition.Status == metav1.ConditionTrue
		if result.Provider == nil || !equality.Semantic.DeepEqual(receipt, previousReceipt) || priorServed != itemServed {
			data := struct {
				Serial   uint32       `json:"serial"`
				ServedAt *metav1.Time `json:"servedAt,omitempty"`
			}{Serial: receipt.FirstSerial}
			if itemServed {
				data.ServedAt = receipt.ServedAt
			}
			result.Provider = providerData(data)
			raw, _ := json.Marshal(receipt)
			result.Provider.State.Raw = raw
		}
		setCondition(&result.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "Hosted DNS record intent is accepted.", item.ObservedGeneration, now)
		recordStatus, recordReason, recordMessage := metav1.ConditionFalse, hostedv1alpha1.ReasonServingPending, pendingMessage(receipt.FirstSerial)
		if itemServed {
			recordStatus, recordReason, recordMessage = metav1.ConditionTrue, "Programmed", "Hosted record revision is served by every fresh hosted server."
		}
		if !item.IsAllowed() && !item.DeletionRequested {
			recordStatus, recordReason, recordMessage = metav1.ConditionUnknown, "Reconciling", "Record intent is not allowed by composition policy."
			setCondition(&result.Conditions, "Accepted", metav1.ConditionFalse, "DeniedByPolicy", recordMessage, item.ObservedGeneration, now)
		}
		setCondition(&result.Conditions, "Programmed", recordStatus, recordReason, recordMessage, item.ObservedGeneration, now)
		unit.Status.RecordSets[i] = result
	}
	aggregateReason := "ProviderChangePending"
	if served {
		aggregateReason = "Programmed"
	}
	setCondition(&unit.Status.Conditions, "Programmed", status, aggregateReason, message, unit.Generation, now)
}

func (r *ZoneReconciler) snapshotRejected(unit *dnsv1alpha1.ZoneUnit, message string, now time.Time) {
	setCondition(&unit.Status.Zone.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "Hosted DNS Zone binding is accepted.", unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&unit.Status.Zone.Conditions, "Programmed", metav1.ConditionFalse, hostedv1alpha1.ReasonSnapshotRejected, message, unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&unit.Status.Conditions, "Programmed", metav1.ConditionFalse, "ProviderInvalidRequest", message, unit.Generation, now)
	// No new record or withdrawal success can be issued for a rejected snapshot.
	prepareRecordReceipts(unit)
	for i := range unit.Status.RecordSets {
		unit.Status.RecordSets[i].DeletionCompleted = false
		setCondition(&unit.Status.RecordSets[i].Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "Hosted DNS record intent is accepted.", unit.Status.RecordSets[i].ObservedGeneration, now)
		setCondition(&unit.Status.RecordSets[i].Conditions, "Programmed", metav1.ConditionFalse, hostedv1alpha1.ReasonServingPending, "Hosted snapshot was rejected.", unit.Status.RecordSets[i].ObservedGeneration, now)
	}
	rejectWithdrawals(unit, now)
}
func (r *ZoneReconciler) reject(unit *dnsv1alpha1.ZoneUnit, rejected acceptance, now time.Time) {
	unit.Status.Zone.Provider = nil
	unit.Status.Zone.NameServers = nil
	setCondition(&unit.Status.Zone.Conditions, "Accepted", rejected.Status, rejected.Reason, rejected.Message, unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&unit.Status.Zone.Conditions, "Programmed", metav1.ConditionUnknown, "Reconciling", rejected.Message, unit.Spec.Zone.ObservedGeneration, now)
	setCondition(&unit.Status.Conditions, "Programmed", metav1.ConditionUnknown, "Reconciling", rejected.Message, unit.Generation, now)
	prepareRecordReceipts(unit)
	for i := range unit.Status.RecordSets {
		result := &unit.Status.RecordSets[i]
		result.DeletionCompleted = false
		setCondition(&result.Conditions, "Accepted", metav1.ConditionFalse, "ZoneNotAccepted", rejected.Message, result.ObservedGeneration, now)
		setCondition(&result.Conditions, "Programmed", metav1.ConditionUnknown, "Reconciling", rejected.Message, result.ObservedGeneration, now)
	}
	rejectWithdrawals(unit, now)
}

func revisionServed(first uint32, digest string, current hostedv1alpha1.HostedZoneSpec, servers []hostedv1alpha1.HostedZoneServerStatus) bool {
	if first == 0 || len(servers) == 0 {
		return false
	}
	for _, server := range servers {
		if server.Serial < first || server.Serial > current.Serial || server.ContentDigest == "" {
			return false
		}
		if server.Serial == first && server.ContentDigest != digest {
			return false
		}
		if server.Serial == current.Serial && server.ContentDigest != current.ContentDigest {
			return false
		}
	}
	return true
}

func prepareRecordReceipts(unit *dnsv1alpha1.ZoneUnit) {
	old := make(map[string]dnsv1alpha1.ZoneUnitRecordSetStatus, len(unit.Status.RecordSets))
	for _, item := range unit.Status.RecordSets {
		old[item.RecordSetNamespace+"/"+item.RecordSetName] = item
	}
	unit.Status.RecordSets = nil
	if len(unit.Spec.RecordSets) > 0 {
		unit.Status.RecordSets = make([]dnsv1alpha1.ZoneUnitRecordSetStatus, len(unit.Spec.RecordSets))
	}
	for i, item := range unit.Spec.RecordSets {
		status := old[item.RecordSetNamespace+"/"+item.RecordSetName]
		if status.RecordSetUID != item.RecordSetUID || status.ObservedGeneration != item.ObservedGeneration {
			status = dnsv1alpha1.ZoneUnitRecordSetStatus{}
		}
		status.RecordSetNamespace, status.RecordSetName, status.RecordSetUID, status.ObservedGeneration = item.RecordSetNamespace, item.RecordSetName, item.RecordSetUID, item.ObservedGeneration
		unit.Status.RecordSets[i] = status
	}
}
