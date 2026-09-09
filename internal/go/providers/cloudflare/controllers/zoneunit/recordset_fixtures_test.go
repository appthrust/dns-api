package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const cloudflareTestRecordSetUID = "4edb2097-97b0-4a1f-92be-2bd8783fe4d8"

func cloudflareRecordSetProvider() *dnsv1alpha1.Provider {
	provider := cloudflareProvider()
	provider.Spec.Versions[0].RecordSet.SupportedTypes = []dnsv1alpha1.RecordType{
		dnsv1alpha1.RecordTypeA,
		dnsv1alpha1.RecordTypeAAAA,
		dnsv1alpha1.RecordTypeTXT,
		dnsv1alpha1.RecordTypeCNAME,
		dnsv1alpha1.RecordTypeMX,
		dnsv1alpha1.RecordTypeCAA,
		dnsv1alpha1.RecordTypeNS,
	}
	return provider
}

func cloudflareProgrammedZone(namespace, name string) *dnsv1alpha1.Zone {
	zone := cloudflareZone(namespace, name)
	raw, err := json.Marshal(cloudflarev1alpha1.CloudflareZoneStatusData{
		Zone: cloudflarev1alpha1.CloudflareZoneStatus{
			ID:     "023e105f4ecef8ad9ca31a8372d0c353",
			Status: "pending",
			Type:   "full",
		},
	})
	if err != nil {
		panic(err)
	}
	zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}
	zone.Status.Conditions = []metav1.Condition{
		{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1},
		{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: 1},
	}
	return zone
}

func cloudflareARecordSet(namespace, name string) *dnsv1alpha1.RecordSet {
	ttl := int32(300)
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: cloudflareTestRecordSetUID, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: cloudflarev1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeA,
			Name:     "www",
			TTL:      &ttl,
			A:        &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10", "192.0.2.11"}},
		},
	}
}

func cloudflareCNAMERecordSet(namespace, name, target string) *dnsv1alpha1.RecordSet {
	ttl := int32(300)
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: cloudflareTestRecordSetUID, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: cloudflarev1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeCNAME,
			Name:     "www",
			TTL:      &ttl,
			CNAME:    &dnsv1alpha1.CNAMERecordSet{Target: target},
		},
	}
}

func cloudflareTXTRecordSetWithOptions(namespace, name string, options map[string]any) *dnsv1alpha1.RecordSet {
	raw, err := json.Marshal(options)
	if err != nil {
		panic(err)
	}
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: cloudflareTestRecordSetUID, Generation: 1},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: cloudflarev1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeTXT,
			Name:     "txt",
			Options:  runtime.RawExtension{Raw: raw},
			TXT:      &dnsv1alpha1.TXTRecordSet{Values: []string{"hello"}},
		},
	}
}

func cloudflareZoneUnitWithRecordSetItem(zoneNamespace, zoneName, recordSetNamespace, recordSetName, recordName string, recordType dnsv1alpha1.RecordType) *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: zoneNamespace, Name: zoneName},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: cloudflarev1alpha1.ProviderRef,
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: zoneNamespace, Name: zoneName},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: "platform", Name: "cloudflare-public"},
			},
			RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
				{
					RecordSetNamespace: recordSetNamespace,
					RecordSetName:      recordSetName,
					RecordSetUID:       cloudflareTestRecordSetUID,
					Name:               recordName,
					Type:               recordType,
				},
			},
		},
	}
}

type fakeCloudflareZoneRecordProvider struct {
	*fakeCloudflareZoneProvider
	*fakeCloudflareRecordSetProvider
}

type fakeCloudflareRecordSetProvider struct {
	created     []CloudflareDNSRecord
	patched     []CloudflareDNSRecord
	deleted     []string
	batches     []CloudflareDNSRecordBatch
	batchErr    error
	records     []CloudflareDNSRecord
	createIDs   []string
	recordsByID map[string]CloudflareDNSRecord
}

func (p *fakeCloudflareRecordSetProvider) ListDNSRecords(_ context.Context, _, name string) ([]CloudflareDNSRecord, error) {
	var out []CloudflareDNSRecord
	for _, record := range p.records {
		if normalizeCloudflareName(record.Name) == normalizeCloudflareName(name) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (p *fakeCloudflareRecordSetProvider) GetDNSRecord(_ context.Context, _, recordID string) (CloudflareDNSRecord, error) {
	if p.recordsByID != nil {
		if record, ok := p.recordsByID[recordID]; ok {
			return record, nil
		}
	}
	for _, record := range p.records {
		if record.ID == recordID {
			return record, nil
		}
	}
	return CloudflareDNSRecord{}, &identityReasonError{reason: "ExternalResourceNotFound", message: "not found"}
}

func (p *fakeCloudflareRecordSetProvider) CreateDNSRecord(_ context.Context, _ string, record CloudflareDNSRecord) (CloudflareDNSRecord, error) {
	if len(p.createIDs) > len(p.created) {
		record.ID = p.createIDs[len(p.created)]
	} else {
		record.ID = fmt.Sprintf("023e105f4ecef8ad9ca31a8372d0c%03d", len(p.created))
	}
	p.created = append(p.created, record)
	p.records = append(p.records, record)
	return record, nil
}

func (p *fakeCloudflareRecordSetProvider) PatchDNSRecord(_ context.Context, _ string, recordID string, record CloudflareDNSRecord) (CloudflareDNSRecord, error) {
	record.ID = recordID
	p.patched = append(p.patched, record)
	for index := range p.records {
		if p.records[index].ID == recordID {
			p.records[index] = record
			return record, nil
		}
	}
	p.records = append(p.records, record)
	return record, nil
}

func (p *fakeCloudflareRecordSetProvider) DeleteDNSRecord(_ context.Context, _, recordID string) error {
	p.deleted = append(p.deleted, recordID)
	p.records = slices.DeleteFunc(p.records, func(record CloudflareDNSRecord) bool {
		return record.ID == recordID
	})
	return nil
}

func (p *fakeCloudflareRecordSetProvider) BatchDNSRecords(ctx context.Context, zoneID string, batch CloudflareDNSRecordBatch) (CloudflareDNSRecordBatch, error) {
	p.batches = append(p.batches, batch)
	if p.batchErr != nil {
		return CloudflareDNSRecordBatch{}, p.batchErr
	}
	out := CloudflareDNSRecordBatch{
		Deletes: slices.Clone(batch.Deletes),
		Patches: make([]CloudflareDNSRecord, 0, len(batch.Patches)),
		Posts:   make([]CloudflareDNSRecord, 0, len(batch.Posts)),
	}
	for _, record := range batch.Deletes {
		if err := p.DeleteDNSRecord(ctx, zoneID, record.ID); err != nil {
			return CloudflareDNSRecordBatch{}, err
		}
	}
	for _, record := range batch.Patches {
		updated, err := p.PatchDNSRecord(ctx, zoneID, record.ID, record)
		if err != nil {
			return CloudflareDNSRecordBatch{}, err
		}
		out.Patches = append(out.Patches, updated)
	}
	for _, record := range batch.Posts {
		created, err := p.CreateDNSRecord(ctx, zoneID, record)
		if err != nil {
			return CloudflareDNSRecordBatch{}, err
		}
		out.Posts = append(out.Posts, created)
	}
	return out, nil
}

func ptrBool(value bool) *bool {
	return &value
}
