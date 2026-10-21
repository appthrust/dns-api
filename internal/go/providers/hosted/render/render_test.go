package render

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
	"k8s.io/apimachinery/pkg/runtime"
)

func paramsFixture() hostedv1alpha1.HostedZoneClassParameters {
	return hostedv1alpha1.HostedZoneClassParameters{NameServers: []hostedv1alpha1.NameServer{{Name: "ns.example.test", Addresses: []string{"2001:db8::53", "192.0.2.53"}}, {Name: "ns.other.test", Addresses: []string{"192.0.2.54"}}}}
}
func unitFixture() *dnsv1alpha1.ZoneUnit {
	ttl := int32(123)
	return &dnsv1alpha1.ZoneUnit{Spec: dnsv1alpha1.ZoneUnitSpec{Zone: dnsv1alpha1.ZoneUnitZoneSpec{DomainName: "example.test"}, RecordSets: []dnsv1alpha1.ZoneUnitRecordSetSpec{
		{Name: "a", Type: dnsv1alpha1.RecordTypeA, TTL: &ttl, A: &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.2", "192.0.2.1", "192.0.2.1"}}},
		{Name: "aaaa", Type: dnsv1alpha1.RecordTypeAAAA, TTL: &ttl, AAAA: &dnsv1alpha1.AAAARecordSet{Addresses: []string{"2001:0db8:0:0::1"}}},
		{Name: "cname", Type: dnsv1alpha1.RecordTypeCNAME, TTL: &ttl, CNAME: &dnsv1alpha1.CNAMERecordSet{Target: "TARGET.Example.TEST."}},
		{Name: "txt", Type: dnsv1alpha1.RecordTypeTXT, TTL: &ttl, TXT: &dnsv1alpha1.TXTRecordSet{Values: []string{"a\"b\\c\n", strings.Repeat("a", 256)}}},
		{Name: "mx", Type: dnsv1alpha1.RecordTypeMX, TTL: &ttl, MX: &dnsv1alpha1.MXRecordSet{Records: []dnsv1alpha1.MXRecord{{Preference: 10, Exchange: "MAIL.Example.TEST"}}}},
		{Name: "caa", Type: dnsv1alpha1.RecordTypeCAA, TTL: &ttl, CAA: &dnsv1alpha1.CAARecordSet{Records: []dnsv1alpha1.CAARecord{{Flags: 0, Tag: "issue", Value: "ca.example"}}}},
		{Name: "child", Type: dnsv1alpha1.RecordTypeNS, TTL: &ttl, NS: &dnsv1alpha1.NSRecordSet{NameServers: []string{"NS.Child.Example.TEST"}}},
	}}}
}
func TestCanonicalSnapshotAndLedgerOmission(t *testing.T) {
	unit := unitFixture()
	params := paramsFixture()
	before := unit.DeepCopy()
	paramsBefore := slices.Clone(params.NameServers[0].Addresses)
	denied := false
	unit.Spec.RecordSets = append(unit.Spec.RecordSets, dnsv1alpha1.ZoneUnitRecordSetSpec{Name: "denied", Allowed: &denied}, dnsv1alpha1.ZoneUnitRecordSetSpec{Name: "deleted", DeletionRequested: true})
	spec, err := Render(unit, params, 0, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Serial != 100 || spec.SOA.MName != "ns.example.test" || spec.SOA.RName != "hostmaster.example.test" || spec.SOA.Minimum != 300 {
		t.Fatalf("spec=%+v", spec)
	}
	values := map[string][]string{}
	for _, record := range spec.Records {
		values[record.Name+"/"+string(record.Type)] = record.Values
	}
	for key, want := range map[string][]string{"a/A": {"192.0.2.1", "192.0.2.2"}, "aaaa/AAAA": {"2001:db8::1"}, "cname/CNAME": {"target.example.test."}, "mx/MX": {"10 mail.example.test."}, "caa/CAA": {"0 issue \"ca.example\""}, "child/NS": {"ns.child.example.test."}, "@/NS": {"ns.example.test.", "ns.other.test."}, "ns/A": {"192.0.2.53"}, "ns/AAAA": {"2001:db8::53"}} {
		if !slices.Equal(values[key], want) {
			t.Fatalf("%s=%q want %q", key, values[key], want)
		}
	}
	if len(spec.Records) != 10 {
		t.Fatalf("RRsets=%d", len(spec.Records))
	}
	if !reflect.DeepEqual(unit.Spec.RecordSets[:7], before.Spec.RecordSets) || !slices.Equal(params.NameServers[0].Addresses, paramsBefore) {
		t.Fatal("renderer mutated input")
	}
	for _, value := range values["txt/TXT"] {
		rr, err := dns.NewRR("txt.example.test. 123 IN TXT " + value)
		if err != nil {
			t.Fatal(err)
		}
		logical := strings.Join(rr.(*dns.TXT).Txt, "")
		if logical != `a\"b\\c\010` && logical != strings.Repeat("a", 256) {
			t.Fatalf("TXT=%q", logical)
		}
	}
}
func TestDigestOrderAndSerial(t *testing.T) {
	unit := unitFixture()
	params := paramsFixture()
	now := time.Unix(100, 0)
	first, err := Render(unit, params, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(unit.Spec.RecordSets)
	slices.Reverse(unit.Spec.RecordSets[6].A.Addresses)
	slices.Reverse(params.NameServers[0].Addresses)
	second, err := Render(unit, params, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentDigest != second.ContentDigest || second.Serial != 101 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	raw, _ := json.Marshal(map[string]any{"contentDigest": first.ContentDigest})
	unit.Status.Zone = &dnsv1alpha1.ZoneUnitZoneStatus{Provider: &dnsv1alpha1.ProviderStatus{Data: runtime.RawExtension{Raw: raw}}}
	unchanged, err := Render(unit, params, 100, time.Unix(200, 0))
	if err != nil || unchanged.Serial != 100 {
		t.Fatalf("unchanged=%+v err=%v", unchanged, err)
	}
	changed := unchanged
	changed.Serial = 900
	if ContentDigest(changed) != unchanged.ContentDigest {
		t.Fatal("serial entered digest")
	}
	changed.SOA.Minimum++
	if ContentDigest(changed) == unchanged.ContentDigest {
		t.Fatal("SOA absent from digest")
	}
	unit.Spec.RecordSets[6].A.Addresses = []string{"192.0.2.9"}
	monotonic, err := Render(unit, params, 500, now)
	if err != nil || monotonic.Serial != 501 {
		t.Fatalf("serial=%d err=%v", monotonic.Serial, err)
	}
	if _, err := Render(unit, params, math.MaxUint32, now); err == nil || err.Error() != "hosted snapshot serial exhausted" {
		t.Fatalf("overflow err=%v", err)
	}
}
func TestDigestCanonicalWireAndInvalidInput(t *testing.T) {
	spec, err := Render(unitFixture(), paramsFixture(), 0, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	copy := spec
	copy.Records = slices.Clone(spec.Records)
	slices.Reverse(copy.Records)
	copy.NameServers = slices.Clone(spec.NameServers)
	slices.Reverse(copy.NameServers)
	if ContentDigest(copy) != spec.ContentDigest {
		t.Fatal("ordering changed digest")
	}
	for i := range copy.Records {
		if copy.Records[i].Type == dnsv1alpha1.RecordTypeCNAME {
			copy.Records[i].Values = []string{"TARGET.EXAMPLE.TEST."}
		}
	}
	if ContentDigest(copy) != spec.ContentDigest {
		t.Fatal("DNS name case changed wire digest")
	}
	copy.Records[0].Type = dnsv1alpha1.RecordTypeA
	copy.Records[0].Values = []string{"invalid RR data"}
	if ContentDigest(copy) != "" {
		t.Fatal("invalid presentation accepted")
	}
	if _, err := Render(nil, paramsFixture(), 0, time.Now()); err == nil {
		t.Fatal("nil unit accepted")
	}
}
