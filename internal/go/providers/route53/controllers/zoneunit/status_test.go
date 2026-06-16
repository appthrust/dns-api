package route53

import (
	"testing"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestZoneUnitProgrammedConditionAggregatesRoute53Targets(t *testing.T) {
	tests := []struct {
		name             string
		zoneStatus       metav1.ConditionStatus
		zoneReason       string
		recordSetStatus  metav1.ConditionStatus
		recordSetReason  string
		omitRecordStatus bool
		wantStatus       metav1.ConditionStatus
		wantReason       string
	}{
		{
			name:            "all targets programmed",
			zoneStatus:      metav1.ConditionTrue,
			zoneReason:      "Programmed",
			recordSetStatus: metav1.ConditionTrue,
			recordSetReason: "Programmed",
			wantStatus:      metav1.ConditionTrue,
			wantReason:      "Programmed",
		},
		{
			name:            "zone false wins",
			zoneStatus:      metav1.ConditionFalse,
			zoneReason:      "ProviderChangePending",
			recordSetStatus: metav1.ConditionTrue,
			recordSetReason: "Programmed",
			wantStatus:      metav1.ConditionFalse,
			wantReason:      "ProviderChangePending",
		},
		{
			name:            "recordset false wins",
			zoneStatus:      metav1.ConditionTrue,
			zoneReason:      "Programmed",
			recordSetStatus: metav1.ConditionFalse,
			recordSetReason: "ProviderConflict",
			wantStatus:      metav1.ConditionFalse,
			wantReason:      "ProviderConflict",
		},
		{
			name:             "missing recordset status is unknown",
			zoneStatus:       metav1.ConditionTrue,
			zoneReason:       "Programmed",
			omitRecordStatus: true,
			wantStatus:       metav1.ConditionUnknown,
			wantReason:       "Reconciling",
		},
		{
			name:            "recordset unknown makes aggregate unknown",
			zoneStatus:      metav1.ConditionTrue,
			zoneReason:      "Programmed",
			recordSetStatus: metav1.ConditionUnknown,
			recordSetReason: "Reconciling",
			wantStatus:      metav1.ConditionUnknown,
			wantReason:      "Reconciling",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit := route53ZoneUnitWithRecordSetItem("app", "apps-example-com", "app", "www-a", "www", dnsv1alpha1.RecordTypeA)
			unit.Generation = 7
			unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{
				Conditions: []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionProgrammed), Status: tt.zoneStatus, Reason: tt.zoneReason, ObservedGeneration: 1},
				},
			}
			if !tt.omitRecordStatus {
				unit.Status.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetStatus{
					{
						RecordSetNamespace: "app",
						RecordSetName:      "www-a",
						Conditions: []metav1.Condition{
							{Type: string(dnsv1alpha1.ConditionProgrammed), Status: tt.recordSetStatus, Reason: tt.recordSetReason, ObservedGeneration: 1},
						},
					},
				}
			}

			setZoneUnitProgrammedCondition(unit)

			assertCondition(t, unit.Status.Conditions, string(dnsv1alpha1.ConditionProgrammed), tt.wantStatus, tt.wantReason)
		})
	}
}
