package webhook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudflarev1alpha1 "github.com/appthrust/dns-api/pkg/go/api/cloudflare/v1alpha1"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

func TestRecordSetValidationRequiresTTLBeforeProviderReconcile(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Spec.TTL = nil

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "spec.ttl is required") {
		t.Fatalf("validateRecordSet error = %v, want ttl requirement", err)
	}
}

func TestZoneUnitValidationRequiresMetadataToMatchZoneRef(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, baseObjects(t)...)

	if err := validator.validateZoneUnit(ctx, validZoneUnit(), nil); err != nil {
		t.Fatalf("validateZoneUnit returned error: %v", err)
	}

	wrongNamespace := validZoneUnit()
	wrongNamespace.Spec.Zone.Ref.Namespace = "other"
	err := validator.validateZoneUnit(ctx, wrongNamespace, nil)
	if err == nil || !strings.Contains(err.Error(), "metadata.namespace must match spec.zone.ref.namespace") {
		t.Fatalf("validateZoneUnit error = %v, want namespace mismatch", err)
	}

	wrongName := validZoneUnit()
	wrongName.Spec.Zone.Ref.Name = "other"
	err = validator.validateZoneUnit(ctx, wrongName, nil)
	if err == nil || !strings.Contains(err.Error(), "metadata.name must match spec.zone.ref.name") {
		t.Fatalf("validateZoneUnit error = %v, want name mismatch", err)
	}
}

func TestZoneUnitUpdateValidationRejectsRecordSetIdentityChanges(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, baseObjects(t)...)

	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.ZoneUnit)
		wantErr string
	}{
		{
			name: "record-name",
			mutate: func(zoneUnit *dnsv1alpha1.ZoneUnit) {
				zoneUnit.Spec.RecordSets[0].Name = "api"
			},
			wantErr: "spec.recordSets[0].name is immutable for RecordSet app/www",
		},
		{
			name: "record-type",
			mutate: func(zoneUnit *dnsv1alpha1.ZoneUnit) {
				zoneUnit.Spec.RecordSets[0].Type = dnsv1alpha1.RecordTypeAAAA
			},
			wantErr: "spec.recordSets[0].type is immutable for RecordSet app/www",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldZoneUnit := validZoneUnitWithRecordSet()
			newZoneUnit := oldZoneUnit.DeepCopy()
			tt.mutate(newZoneUnit)

			response := validator.Handle(ctx, zoneUnitUpdateRequest(t, newZoneUnit, oldZoneUnit))

			if response.Allowed {
				t.Fatalf("ZoneUnit update was allowed, want denied")
			}
			if !strings.Contains(response.Result.Message, tt.wantErr) {
				t.Fatalf("response message = %q, want %q", response.Result.Message, tt.wantErr)
			}
		})
	}
}

func TestZoneUnitUpdateValidationAllowsRecordSetBodyChanges(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, baseObjects(t)...)
	oldZoneUnit := validZoneUnitWithRecordSet()
	newZoneUnit := oldZoneUnit.DeepCopy()
	newZoneUnit.Spec.RecordSets[0].TTL = ptr(int32(60))
	newZoneUnit.Spec.RecordSets[0].A.Addresses = []string{"192.0.2.20"}

	response := validator.Handle(ctx, zoneUnitUpdateRequest(t, newZoneUnit, oldZoneUnit))

	if !response.Allowed {
		t.Fatalf("ZoneUnit update was denied: %s", response.Result.Message)
	}
}

func TestZoneUnitDeleteValidationRejectsDeleteWhileOwningZoneExists(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, baseObjects(t)...)

	response := validator.Handle(ctx, zoneUnitDeleteRequest(t, validZoneUnit()))

	if response.Allowed {
		t.Fatalf("ZoneUnit delete was allowed, want denied")
	}
	if !strings.Contains(response.Result.Message, "ZoneUnit cannot be deleted while the owning Zone exists and is not deleting") {
		t.Fatalf("response message = %q", response.Result.Message)
	}
}

func TestZoneUnitDeleteValidationAllowsDeleteWhenOwningZoneIsMissing(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, baseObjectsWithoutZone(t)...)

	response := validator.Handle(ctx, zoneUnitDeleteRequest(t, validZoneUnit()))

	if !response.Allowed {
		t.Fatalf("ZoneUnit delete was denied: %s", response.Result.Message)
	}
}

func TestRecordSetValidationAppliesExtensionCELRules(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Spec.TTL = nil
	recordSet.Spec.A = nil
	recordSet.Spec.Options = raw(t, map[string]any{
		"alias": map[string]any{
			"dnsName":              "dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.",
			"hostedZoneID":         "Z14GRHDCWA56QT",
			"evaluateTargetHealth": true,
		},
	})

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}

	recordSet.Spec.TTL = ptr(int32(300))
	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "ttl must not be specified") {
		t.Fatalf("validateRecordSet error = %v, want alias ttl CEL failure", err)
	}
}

func TestRecordSetValidationAppliesCloudflareFixedTTLRules(t *testing.T) {
	ctx := context.Background()
	provider := builtInProviderManifest(t, "cloudflare_extensions.yaml")
	wantRangeError := "cloudflare fixed ttl must be between 60 and 86400 seconds"
	tests := map[string]struct {
		ttl     *int32
		options runtime.RawExtension
		wantErr string
	}{
		"auto ttl option": {
			options: raw(t, map[string]any{"ttl": "Auto"}),
		},
		"lower bound": {
			ttl: ptr(int32(60)),
		},
		"upper bound": {
			ttl: ptr(int32(86400)),
		},
		"auto ttl sentinel is rejected as fixed ttl": {
			ttl:     ptr(int32(1)),
			wantErr: wantRangeError,
		},
		"below lower bound": {
			ttl:     ptr(int32(59)),
			wantErr: wantRangeError,
		},
		"above upper bound": {
			ttl:     ptr(int32(86401)),
			wantErr: wantRangeError,
		},
		"max int": {
			ttl:     ptr(int32(2147483647)),
			wantErr: wantRangeError,
		},
		"auto ttl option with spec ttl": {
			ttl:     ptr(int32(1)),
			options: raw(t, map[string]any{"ttl": "Auto"}),
			wantErr: "ttl must not be specified when cloudflare automatic ttl is used",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			recordSet := validARecordSet()
			recordSet.Spec.Provider = cloudflarev1alpha1.ProviderRef
			recordSet.Spec.TTL = tt.ttl
			recordSet.Spec.Options = tt.options
			validator := newTestValidator(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}}, provider.DeepCopy(), recordSet)

			err := validator.validateRecordSet(ctx, recordSet)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRecordSet returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsRoute53AliasURLDNSName(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Spec.TTL = nil
	recordSet.Spec.A = nil
	recordSet.Spec.Options = raw(t, map[string]any{
		"alias": map[string]any{
			"dnsName":              "https://not-a-dns-name.example.com.",
			"hostedZoneID":         "Z14GRHDCWA56QT",
			"evaluateTargetHealth": false,
		},
	})

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "spec.options") {
		t.Fatalf("validateRecordSet error = %v, want alias dnsName schema failure", err)
	}
}

func TestRecordSetValidationAcceptsStandardAAAAForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validAAAARecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsStandardTXTForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validTXTRecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsStandardCNAMEForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validCNAMERecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsStandardMXForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validMXRecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsStandardCAAForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validCAARecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsDelegatedNSForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validNSRecordSet()

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsNullMXForRoute53(t *testing.T) {
	ctx := context.Background()
	recordSet := validMXRecordSet()
	recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{{Preference: 0, Exchange: "."}}

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsTXTNamesWithUnderscore(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"_acme-challenge", "_dmarc", "selector._domainkey", "_acme-challenge.api"} {
		t.Run(name, func(t *testing.T) {
			recordSet := validTXTRecordSet()
			recordSet.Spec.Name = name

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			if err := validator.validateRecordSet(ctx, recordSet); err != nil {
				t.Fatalf("validateRecordSet returned error: %v", err)
			}
		})
	}
}

func TestRecordSetValidationRejectsNonPrintableASCIITXTValues(t *testing.T) {
	ctx := context.Background()
	tests := map[string]string{
		"newline":   "line1\nline2",
		"tab":       "one\ttwo",
		"control":   "prefix\x7fsuffix",
		"non-ascii": "exämple",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			recordSet := validTXTRecordSet()
			recordSet.Spec.TXT.Values = []string{value}
			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if err == nil || !strings.Contains(err.Error(), "spec.txt.values[0] must contain only printable ASCII characters") {
				t.Fatalf("validateRecordSet error = %v, want printable ASCII failure", err)
			}
		})
	}
}

func TestRecordSetValidationCanDisableNonPrintableASCIITXTValidation(t *testing.T) {
	ctx := context.Background()
	recordSet := validTXTRecordSet()
	recordSet.Spec.TXT.Values = []string{"line1\nline2"}
	provider := route53Provider(t)
	provider.Spec.Versions[0].RecordSet.DisableValidations = append(
		provider.Spec.Versions[0].RecordSet.DisableValidations,
		dnsv1alpha1.RecordSetValidationToggle{
			Name: "forbid-txt-non-printable-ascii",
			When: "self.spec.type == 'TXT'",
		},
	)
	validator := newTestValidator(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		provider,
		route53ZoneClass(t),
		recordSet,
	)

	if err := validator.validateRecordSet(ctx, recordSet); err != nil {
		t.Fatalf("validateRecordSet returned error: %v", err)
	}
}

func TestRecordSetValidationAcceptsCNAMENamesWithUnderscore(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"_acme-challenge", "selector._domainkey", "_verify.www"} {
		t.Run(name, func(t *testing.T) {
			recordSet := validCNAMERecordSet()
			recordSet.Spec.Name = name

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			if err := validator.validateRecordSet(ctx, recordSet); err != nil {
				t.Fatalf("validateRecordSet returned error: %v", err)
			}
		})
	}
}

func TestRecordSetValidationRejectsInvalidCNAME(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.RecordSet)
		wantErr string
	}{
		{name: "apex", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.Name = "@" }, wantErr: "@ is not allowed"},
		{name: "trailing-dot", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CNAME.Target = "target.example.net." }, wantErr: "without a trailing root dot"},
		{name: "uppercase", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CNAME.Target = "Target.example.net" }, wantErr: "lowercase ASCII"},
		{name: "ip", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CNAME.Target = "192.0.2.10" }, wantErr: "not an IP address"},
		{name: "url", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CNAME.Target = "https://target.example.net" }, wantErr: "lowercase ASCII"},
		{name: "a-body", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.A = &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}
		}, wantErr: "spec.a must not be specified for CNAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validCNAMERecordSet()
			tt.mutate(recordSet)

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsInvalidMX(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.RecordSet)
		wantErr string
	}{
		{name: "empty", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.MX.Records = nil }, wantErr: "spec.mx.records is required"},
		{name: "duplicate", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{
				{Preference: 10, Exchange: "mail.example.net"},
				{Preference: 10, Exchange: "mail.example.net"},
			}
		}, wantErr: "duplicate preference and exchange pairs"},
		{name: "same-preference-different-exchange", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{
				{Preference: 10, Exchange: "mail1.example.net"},
				{Preference: 10, Exchange: "mail2.example.net"},
			}
		}, wantErr: ""},
		{name: "uppercase", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.MX.Records[0].Exchange = "Mail.example.net" }, wantErr: "lowercase ASCII"},
		{name: "trailing-dot", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.MX.Records[0].Exchange = "mail.example.net." }, wantErr: "without a trailing root dot"},
		{name: "ip", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.MX.Records[0].Exchange = "192.0.2.10" }, wantErr: "not an IP address"},
		{name: "url", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.MX.Records[0].Exchange = "https://mail.example.net"
		}, wantErr: "lowercase ASCII"},
		{name: "null-preference", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{{Preference: 10, Exchange: "."}}
		}, wantErr: "preference must be 0"},
		{name: "null-combined", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.MX.Records = []dnsv1alpha1.MXRecord{
				{Preference: 0, Exchange: "."},
				{Preference: 10, Exchange: "mail.example.net"},
			}
		}, wantErr: "only one record"},
		{name: "a-body", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.A = &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}
		}, wantErr: "spec.a must not be specified for MX"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validMXRecordSet()
			tt.mutate(recordSet)

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRecordSet returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsInvalidCAA(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.RecordSet)
		wantErr string
	}{
		{name: "empty", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CAA.Records = nil }, wantErr: "spec.caa.records is required"},
		{name: "duplicate", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.CAA.Records = []dnsv1alpha1.CAARecord{
				{Flags: 0, Tag: "issue", Value: "letsencrypt.org"},
				{Flags: 0, Tag: "issue", Value: "letsencrypt.org"},
			}
		}, wantErr: "duplicate flags, tag, and value tuples"},
		{name: "same-tag-different-value", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.CAA.Records = []dnsv1alpha1.CAARecord{
				{Flags: 0, Tag: "issue", Value: "letsencrypt.org"},
				{Flags: 0, Tag: "issue", Value: "pki.goog"},
			}
		}, wantErr: ""},
		{name: "flags-high", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CAA.Records[0].Flags = 256 }, wantErr: "flags must be in range 0..255"},
		{name: "uppercase-tag", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CAA.Records[0].Tag = "Issue" }, wantErr: "lowercase ASCII alphanumeric"},
		{name: "hyphen-tag", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CAA.Records[0].Tag = "issue-wild" }, wantErr: "lowercase ASCII alphanumeric"},
		{name: "empty-value", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.CAA.Records[0].Value = "" }, wantErr: "value is required"},
		{name: "a-body", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.A = &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}
		}, wantErr: "spec.a must not be specified for CAA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validCAARecordSet()
			tt.mutate(recordSet)

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateRecordSet returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsInvalidDelegatedNS(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.RecordSet)
		wantErr string
	}{
		{name: "apex", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.Name = "@" }, wantErr: "@ is not allowed"},
		{name: "wildcard", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.Name = "*.team-a" }, wantErr: "wildcard is not allowed"},
		{name: "empty", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.NS.NameServers = nil }, wantErr: "spec.ns.nameServers is required"},
		{name: "duplicate", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.NS.NameServers = []string{"ns-111.example-dns.net", "ns-111.example-dns.net"}
		}, wantErr: "duplicate name servers"},
		{name: "uppercase", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.NS.NameServers[0] = "NS-111.example-dns.net" }, wantErr: "lowercase ASCII"},
		{name: "trailing-dot", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.NS.NameServers[0] = "ns-111.example-dns.net." }, wantErr: "without a trailing root dot"},
		{name: "ip", mutate: func(recordSet *dnsv1alpha1.RecordSet) { recordSet.Spec.NS.NameServers[0] = "192.0.2.10" }, wantErr: "not an IP address"},
		{name: "url", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.NS.NameServers[0] = "https://ns-111.example-dns.net"
		}, wantErr: "lowercase ASCII"},
		{name: "a-body", mutate: func(recordSet *dnsv1alpha1.RecordSet) {
			recordSet.Spec.A = &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}}
		}, wantErr: "spec.a must not be specified for NS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validNSRecordSet()
			tt.mutate(recordSet)

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsInvalidTXTValues(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		values  []string
		wantErr string
	}{
		{name: "empty", values: []string{""}, wantErr: "must not be empty"},
		{name: "duplicate", values: []string{"challenge-token", "challenge-token"}, wantErr: "duplicate TXT values"},
		{name: "too-long", values: []string{strings.Repeat("a", 4001)}, wantErr: "4000 UTF-8 octets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validTXTRecordSet()
			recordSet.Spec.TXT.Values = tt.values

			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSet(ctx, recordSet)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetValidationRejectsUnderscoreForARecord(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Spec.Name = "_acme-challenge"

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "invalid for A records") {
		t.Fatalf("validateRecordSet error = %v, want A record name failure", err)
	}
}

func TestRecordSetValidationRejectsAAAAWithIPv4Address(t *testing.T) {
	ctx := context.Background()
	recordSet := validAAAARecordSet()
	recordSet.Spec.AAAA.Addresses = []string{"192.0.2.10"}

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "must be an IPv6 address") {
		t.Fatalf("validateRecordSet error = %v, want IPv6 family failure", err)
	}
}

func TestRecordSetValidationRejectsDuplicateAAAAByParsedAddress(t *testing.T) {
	ctx := context.Background()
	recordSet := validAAAARecordSet()
	recordSet.Spec.AAAA.Addresses = []string{"2001:db8::10", "2001:0db8:0000::0010"}

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "duplicate IP addresses") {
		t.Fatalf("validateRecordSet error = %v, want parsed duplicate failure", err)
	}
}

func TestRecordSetValidationRejectsTypeOutsideExtensionSupportedTypes(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Spec.Type = dnsv1alpha1.RecordTypeAAAA
	extension := route53Provider(t)
	extension.Spec.Versions[0].RecordSet.SupportedTypes = []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA}

	validator := newTestValidator(t,
		append(baseObjectsWithoutProvider(t), extension, validZone(), recordSet)...,
	)

	err := validator.validateRecordSet(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), `spec.type "AAAA" is not supported`) {
		t.Fatalf("validateRecordSet error = %v, want supportedTypes failure", err)
	}
}

func TestRecordSetValidationChecksIntrinsicNameWithoutZone(t *testing.T) {
	ctx := context.Background()

	t.Run("A rejects underscore", func(t *testing.T) {
		recordSet := validARecordSet()
		recordSet.Spec.ZoneRef.Name = "missing-zone"
		recordSet.Spec.Name = "_acme-challenge"

		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), recordSet)...)

		err := validator.validateRecordSet(ctx, recordSet)
		if err == nil || !strings.Contains(err.Error(), "invalid for A records") {
			t.Fatalf("validateRecordSet error = %v, want intrinsic name failure", err)
		}
	})

	t.Run("CNAME rejects apex by default", func(t *testing.T) {
		recordSet := validCNAMERecordSet()
		recordSet.Spec.ZoneRef.Name = "missing-zone"
		recordSet.Spec.Name = "@"

		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), recordSet)...)

		err := validator.validateRecordSet(ctx, recordSet)
		if err == nil || !strings.Contains(err.Error(), "@ is not allowed for CNAME") {
			t.Fatalf("validateRecordSet error = %v, want CNAME apex failure", err)
		}
	})

	t.Run("CNAME apex can be provider-disabled", func(t *testing.T) {
		recordSet := validCNAMERecordSet()
		recordSet.Spec.ZoneRef.Name = "missing-zone"
		recordSet.Spec.Name = "@"
		provider := route53Provider(t)
		provider.Spec.Versions[0].RecordSet.DisableValidations = append(
			provider.Spec.Versions[0].RecordSet.DisableValidations,
			dnsv1alpha1.RecordSetValidationToggle{
				Name: "forbid-cname-apex",
				When: "self.spec.type == 'CNAME' && self.spec.name == '@'",
			},
		)

		validator := newTestValidator(t,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
			provider,
			route53ZoneClass(t),
			recordSet,
		)

		if err := validator.validateRecordSet(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSet returned error: %v", err)
		}
	})
}

func TestZoneValidationChecksAdoptionSchema(t *testing.T) {
	ctx := context.Background()
	zone := validZone()
	zone.Spec.Adoption = raw(t, map[string]any{"hostedZoneId": "/hostedzone/Z10219583PPQMV0U1KGN2"})

	validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

	err := validator.validateZone(ctx, zone, nil)
	if err == nil || !strings.Contains(err.Error(), "spec.adoption") {
		t.Fatalf("validateZone error = %v, want adoption schema failure", err)
	}
}

func TestAllowedRecordSetsRequireSelectorRecordsAndTypes(t *testing.T) {
	err := validateAllowedRecordSets([]dnsv1alpha1.AllowedRecordSet{
		{
			Records: []dnsv1alpha1.AllowedRecord{
				{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: ".*"},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "namespaces.selector must not be empty") {
		t.Fatalf("validateAllowedRecordSets error = %v, want empty selector failure", err)
	}

	err = validateAllowedRecordSets([]dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{
					Name: dnsv1alpha1.RecordNamePolicy{Pattern: ".*"},
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "types must not be empty") {
		t.Fatalf("validateAllowedRecordSets error = %v, want empty types failure", err)
	}
}

func TestRecordSetValidationAllowsRecordAccessPolicyMismatch(t *testing.T) {
	ctx := context.Background()
	zone := validZone()
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: "www"},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
				},
			},
		},
	}

	tests := []struct {
		name            string
		namespaceLabels map[string]string
		recordName      string
		recordType      dnsv1alpha1.RecordType
	}{
		{
			name:            "namespace",
			namespaceLabels: nil,
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeA,
		},
		{
			name:            "record-name",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "api",
			recordType:      dnsv1alpha1.RecordTypeA,
		},
		{
			name:            "record-type",
			namespaceLabels: map[string]string{"access": "dns"},
			recordName:      "www",
			recordType:      dnsv1alpha1.RecordTypeAAAA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validARecordSet()
			recordSet.ObjectMeta.Namespace = "record-ns"
			recordSet.Spec.ZoneRef.Namespace = ptr("app")
			recordSet.Spec.Name = tt.recordName
			recordSet.Spec.Type = tt.recordType
			if tt.recordType == dnsv1alpha1.RecordTypeAAAA {
				recordSet.Spec.A = nil
				recordSet.Spec.AAAA = &dnsv1alpha1.AAAARecordSet{Addresses: []string{"2001:db8::10"}}
			}

			validator := newTestValidator(t,
				append(baseObjectsWithoutZone(t),
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns", Labels: tt.namespaceLabels}},
					zone,
					recordSet,
				)...,
			)

			if err := validator.validateRecordSet(ctx, recordSet); err != nil {
				t.Fatalf("validateRecordSet returned error: %v", err)
			}
		})
	}
}

func TestZoneUpdateAllowsPolicyThatWouldDisallowCrossNamespaceRecordSet(t *testing.T) {
	ctx := context.Background()
	oldZone := validZone()
	oldZone.ObjectMeta.Namespace = "app"
	oldZone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: ".*"},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA},
				},
			},
		},
	}
	newZone := oldZone.DeepCopy()
	newZone.Spec.AllowedRecordSets[0].Records[0].Name.Pattern = "bar"

	recordSet := validARecordSet()
	recordSet.ObjectMeta.Namespace = "record-ns"
	recordSet.Spec.ZoneRef.Namespace = ptr("app")
	recordSet.Spec.Name = "foo"

	validator := newTestValidator(t,
		append(baseObjectsWithoutZone(t),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "record-ns", Labels: map[string]string{"access": "dns"}}},
			oldZone,
			recordSet,
		)...,
	)

	if err := validator.validateZone(ctx, newZone, oldZone); err != nil {
		t.Fatalf("validateZone returned error: %v", err)
	}
}

func TestZoneValidationUsesProviderWhenZoneClassIsMissing(t *testing.T) {
	ctx := context.Background()
	zone := validZone()
	zone.Spec.ZoneClassRef.Name = "missing-zoneclass"
	zone.Spec.Adoption = raw(t, map[string]any{"hostedZoneId": "Z1234567890"})

	validator := newTestValidator(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		route53Provider(t),
		zone,
	)

	if err := validator.validateZone(ctx, zone, nil); err != nil {
		t.Fatalf("validateZone returned error: %v", err)
	}
}

func TestZoneValidationAllowsProviderMismatchWithZoneClass(t *testing.T) {
	ctx := context.Background()
	zone := validZone()
	zone.Spec.Provider = dnsv1alpha1.ProviderReference{Name: "other-provider.example", Version: "v1alpha1"}
	otherProvider := route53Provider(t)
	otherProvider.Name = "other-provider.example"

	validator := newTestValidator(t,
		append(baseObjectsWithoutProvider(t), route53Provider(t), otherProvider, zone)...,
	)

	if err := validator.validateZone(ctx, zone, nil); err != nil {
		t.Fatalf("validateZone returned error: %v", err)
	}
}

func TestZoneValidationRejectsRecordAccessTypeOutsideStandardOrExtensionTypes(t *testing.T) {
	ctx := context.Background()
	zone := validZone()
	zone.Spec.AllowedRecordSets = []dnsv1alpha1.AllowedRecordSet{
		{
			Namespaces: dnsv1alpha1.AllowedRecordSetNamespaces{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"access": "dns"}},
			},
			Records: []dnsv1alpha1.AllowedRecord{
				{
					Name:  dnsv1alpha1.RecordNamePolicy{Pattern: "www"},
					Types: []dnsv1alpha1.RecordType{dnsv1alpha1.RecordType("HTTPS")},
				},
			},
		},
	}

	validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

	err := validator.validateZone(ctx, zone, nil)
	if err == nil || !strings.Contains(err.Error(), "is not supported by core API or any Provider") {
		t.Fatalf("validateZone error = %v, want unsupported record access type", err)
	}
}

func TestRecordSetStatusValidationChecksProviderDataSchema(t *testing.T) {
	ctx := context.Background()
	recordSet := validARecordSet()
	recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{
		Data: raw(t, map[string]any{
			"hostedZoneID": "Z000001",
			"recordName":   "www.apps.example.com.",
			"recordType":   "HTTPS",
		}),
	}

	validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

	err := validator.validateRecordSetStatus(ctx, recordSet)
	if err == nil || !strings.Contains(err.Error(), "status.provider.data") {
		t.Fatalf("validateRecordSetStatus error = %v, want status schema failure", err)
	}
}

func TestRecordSetStatusValidationChecksObservedGenerationAndZoneRef(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts defaulted Zone ref", func(t *testing.T) {
		recordSet := validARecordSet()
		recordSet.Generation = 2
		recordSet.Status.ObservedGeneration = 2
		recordSet.Status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
			Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
		}
		validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

		if err := validator.validateRecordSetStatus(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSetStatus returned error: %v", err)
		}
	})

	t.Run("accepts explicit Zone ref namespace", func(t *testing.T) {
		zoneNamespace := "platform"
		recordSet := validARecordSet()
		recordSet.Spec.ZoneRef.Namespace = &zoneNamespace
		recordSet.Status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
			Ref: dnsv1alpha1.ObjectReference{Namespace: "platform", Name: "apps-example-com"},
		}
		validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

		if err := validator.validateRecordSetStatus(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSetStatus returned error: %v", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.RecordSet)
		wantErr string
	}{
		{
			name: "rejects future observed generation",
			mutate: func(recordSet *dnsv1alpha1.RecordSet) {
				recordSet.Generation = 1
				recordSet.Status.ObservedGeneration = 2
			},
			wantErr: "status.observedGeneration",
		},
		{
			name: "rejects different Zone ref namespace",
			mutate: func(recordSet *dnsv1alpha1.RecordSet) {
				recordSet.Status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
					Ref: dnsv1alpha1.ObjectReference{Namespace: "other", Name: "apps-example-com"},
				}
			},
			wantErr: "status.zone.ref.namespace",
		},
		{
			name: "rejects different Zone ref name",
			mutate: func(recordSet *dnsv1alpha1.RecordSet) {
				recordSet.Status.Zone = &dnsv1alpha1.RecordSetZoneStatus{
					Ref: dnsv1alpha1.ObjectReference{Namespace: "app", Name: "other"},
				}
			},
			wantErr: "status.zone.ref.name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recordSet := validARecordSet()
			tt.mutate(recordSet)
			validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

			err := validator.validateRecordSetStatus(ctx, recordSet)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateRecordSetStatus error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRecordSetStatusWebhookRejectsControllerNameAndExtraRefFields(t *testing.T) {
	ctx := context.Background()
	validator := newTestValidator(t, append(baseObjects(t), validARecordSet())...)

	t.Run("rejects controllerName", func(t *testing.T) {
		response := validator.Handle(ctx, recordSetStatusRequest(t, map[string]any{
			"ref":            map[string]any{"namespace": "app", "name": "apps-example-com"},
			"controllerName": "route53.dns.appthrust.io/controller",
		}))

		if response.Allowed {
			t.Fatalf("RecordSet status update was allowed, want denied")
		}
		if !strings.Contains(response.Result.Message, "status.zone.controllerName") {
			t.Fatalf("response message = %q, want status.zone.controllerName", response.Result.Message)
		}
	})

	t.Run("rejects extra ref field", func(t *testing.T) {
		response := validator.Handle(ctx, recordSetStatusRequest(t, map[string]any{
			"ref": map[string]any{"namespace": "app", "name": "apps-example-com", "kind": "Zone"},
		}))

		if response.Allowed {
			t.Fatalf("RecordSet status update was allowed, want denied")
		}
		if !strings.Contains(response.Result.Message, "status.zone.ref.kind") {
			t.Fatalf("response message = %q, want status.zone.ref.kind", response.Result.Message)
		}
	})
}

func TestZoneStatusValidationChecksConditionsAndNameServers(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts core reasons and canonical name servers", func(t *testing.T) {
		zone := validZone()
		zone.Status.NameServers = []string{"ns-1.example.net", "ns-2.example.net"}
		zone.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted"},
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionTrue, Reason: "Programmed"},
		}
		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

		if err := validator.validateZoneStatus(ctx, zone); err != nil {
			t.Fatalf("validateZoneStatus returned error: %v", err)
		}
	})

	t.Run("rejects invalid name servers", func(t *testing.T) {
		zone := validZone()
		zone.Status.NameServers = []string{"NS-1.example.net"}
		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

		err := validator.validateZoneStatus(ctx, zone)
		if err == nil || !strings.Contains(err.Error(), "status.nameServers[0]") {
			t.Fatalf("validateZoneStatus error = %v, want name server failure", err)
		}
	})

	t.Run("rejects duplicate name servers", func(t *testing.T) {
		zone := validZone()
		zone.Status.NameServers = []string{"ns-1.example.net", "ns-1.example.net"}
		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

		err := validator.validateZoneStatus(ctx, zone)
		if err == nil || !strings.Contains(err.Error(), "duplicate name servers") {
			t.Fatalf("validateZoneStatus error = %v, want duplicate name server failure", err)
		}
	})

	t.Run("rejects future observed generation", func(t *testing.T) {
		zone := validZone()
		zone.Generation = 1
		zone.Status.ObservedGeneration = 2
		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

		err := validator.validateZoneStatus(ctx, zone)
		if err == nil || !strings.Contains(err.Error(), "status.observedGeneration") {
			t.Fatalf("validateZoneStatus error = %v, want observedGeneration failure", err)
		}
	})

	t.Run("rejects reason from another resource contract", func(t *testing.T) {
		zone := validZone()
		zone.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionFalse, Reason: "ProviderChangeDeferred"},
		}
		validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

		err := validator.validateZoneStatus(ctx, zone)
		if err == nil || !strings.Contains(err.Error(), "status.conditions[0].reason") {
			t.Fatalf("validateZoneStatus error = %v, want condition reason failure", err)
		}
	})

	t.Run("rejects proposal-only reasons", func(t *testing.T) {
		for _, reason := range []string{"UnsupportedProvider", "ProviderRejected", "InvalidProviderOptions", "NotAccepted"} {
			t.Run(reason, func(t *testing.T) {
				zone := validZone()
				zone.Status.Conditions = []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionFalse, Reason: reason},
				}
				validator := newTestValidator(t, append(baseObjectsWithoutZone(t), zone)...)

				err := validator.validateZoneStatus(ctx, zone)
				if err == nil || !strings.Contains(err.Error(), "status.conditions[0].reason") {
					t.Fatalf("validateZoneStatus error = %v, want proposal-only reason failure", err)
				}
			})
		}
	})

	t.Run("accepts provider additional reasons", func(t *testing.T) {
		provider := route53Provider(t)
		provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
			{
				ConditionType: dnsv1alpha1.ConditionProgrammed,
				Status:        metav1.ConditionFalse,
				Reason:        "Route53DelegationPending",
				Description:   "Route 53 has not finished delegation.",
			},
		}
		zone := validZone()
		zone.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionFalse, Reason: "Route53DelegationPending"},
		}
		validator := newTestValidator(t, append(baseObjectsWithoutProvider(t), provider, zone)...)

		if err := validator.validateZoneStatus(ctx, zone); err != nil {
			t.Fatalf("validateZoneStatus returned error: %v", err)
		}
	})
}

func TestRecordSetStatusValidationChecksConditions(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects reason from zone contract", func(t *testing.T) {
		recordSet := validARecordSet()
		recordSet.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionFalse, Reason: "ZoneClassNotAccepted"},
		}
		validator := newTestValidator(t, append(baseObjects(t), recordSet)...)

		err := validator.validateRecordSetStatus(ctx, recordSet)
		if err == nil || !strings.Contains(err.Error(), "status.conditions[0].reason") {
			t.Fatalf("validateRecordSetStatus error = %v, want condition reason failure", err)
		}
	})

	t.Run("accepts provider additional reasons", func(t *testing.T) {
		provider := route53Provider(t)
		provider.Spec.Versions[0].RecordSet.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
			{
				ConditionType: dnsv1alpha1.ConditionProgrammed,
				Status:        metav1.ConditionFalse,
				Reason:        "Route53BatchDeferred",
				Description:   "Route 53 batch is deferred.",
			},
		}
		recordSet := validARecordSet()
		recordSet.Status.Conditions = []metav1.Condition{
			{Type: string(dnsv1alpha1.ConditionProgrammed), Status: metav1.ConditionFalse, Reason: "Route53BatchDeferred"},
		}
		validator := newTestValidator(t, append(baseObjectsWithoutProvider(t), provider, validZone(), recordSet)...)

		if err := validator.validateRecordSetStatus(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSetStatus returned error: %v", err)
		}
	})
}

func TestZoneClassStatusValidationRejectsInvalidConditions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*dnsv1alpha1.ZoneClass)
		wantErr string
	}{
		{
			name: "unknown type",
			mutate: func(zoneClass *dnsv1alpha1.ZoneClass) {
				zoneClass.Status.Conditions = []metav1.Condition{
					{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"},
				}
			},
			wantErr: "type",
		},
		{
			name: "unknown reason",
			mutate: func(zoneClass *dnsv1alpha1.ZoneClass) {
				zoneClass.Status.Conditions = []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Forged"},
				}
			},
			wantErr: "reason",
		},
		{
			name: "future observed generation",
			mutate: func(zoneClass *dnsv1alpha1.ZoneClass) {
				zoneClass.Status.Conditions = []metav1.Condition{
					{Type: string(dnsv1alpha1.ConditionAccepted), Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 2},
				}
			},
			wantErr: "observedGeneration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zoneClass := route53ZoneClass(t)
			zoneClass.Generation = 1
			tt.mutate(zoneClass)

			err := validateZoneClassStatus(zoneClass)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateZoneClassStatus error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCloudflareProviderIDSchemaValidation(t *testing.T) {
	ctx := context.Background()
	const validID = "023e105f4ecef8ad9ca31a8372d0c353"
	provider := builtInProviderManifest(t, "cloudflare_extensions.yaml")
	validator := newTestValidator(t, &provider)

	t.Run("zone adoption", func(t *testing.T) {
		cases := map[string]string{
			"uppercase": "023e105f4ecef8ad9ca31a8372d0C353",
			"non hex":   "023e105f4ecef8ad9ca31a8372d0c35z",
			"short":     "023e105f4ecef8ad9ca31a8372d0c35",
			"long":      "023e105f4ecef8ad9ca31a8372d0c3530",
		}
		for name, id := range cases {
			t.Run(name, func(t *testing.T) {
				zone := validZone()
				zone.Spec.Provider = cloudflarev1alpha1.ProviderRef
				zone.Spec.Adoption = raw(t, map[string]any{"zoneID": id})

				err := validator.validateZone(ctx, zone, nil)
				if err == nil || !strings.Contains(err.Error(), "spec.adoption") {
					t.Fatalf("validateZone error = %v, want Cloudflare zoneID schema failure", err)
				}
			})
		}
		zone := validZone()
		zone.Spec.Provider = cloudflarev1alpha1.ProviderRef
		zone.Spec.Adoption = raw(t, map[string]any{"zoneID": validID})
		if err := validator.validateZone(ctx, zone, nil); err != nil {
			t.Fatalf("validateZone returned error for valid Cloudflare zoneID: %v", err)
		}
	})

	t.Run("recordset adoption", func(t *testing.T) {
		cases := map[string][]string{
			"uppercase": {"023e105f4ecef8ad9ca31a8372d0C353"},
			"non hex":   {"023e105f4ecef8ad9ca31a8372d0c35z"},
			"short":     {"023e105f4ecef8ad9ca31a8372d0c35"},
			"long":      {"023e105f4ecef8ad9ca31a8372d0c3530"},
			"duplicate": {validID, validID},
		}
		for name, ids := range cases {
			t.Run(name, func(t *testing.T) {
				recordSet := validARecordSet()
				recordSet.Spec.Provider = cloudflarev1alpha1.ProviderRef
				recordSet.Spec.Adoption = raw(t, map[string]any{"recordIDs": ids})

				err := validator.validateRecordSet(ctx, recordSet)
				if err == nil || !strings.Contains(err.Error(), "spec.adoption") {
					t.Fatalf("validateRecordSet error = %v, want Cloudflare recordIDs schema failure", err)
				}
			})
		}
		recordSet := validARecordSet()
		recordSet.Spec.Provider = cloudflarev1alpha1.ProviderRef
		recordSet.Spec.Adoption = raw(t, map[string]any{"recordIDs": []string{validID}})
		if err := validator.validateRecordSet(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSet returned error for valid Cloudflare recordIDs: %v", err)
		}
	})

	t.Run("zone status data", func(t *testing.T) {
		for _, id := range []string{
			"023e105f4ecef8ad9ca31a8372d0C353",
			"023e105f4ecef8ad9ca31a8372d0c35z",
			"023e105f4ecef8ad9ca31a8372d0c35",
			"023e105f4ecef8ad9ca31a8372d0c3530",
		} {
			zone := validZone()
			zone.Spec.Provider = cloudflarev1alpha1.ProviderRef
			zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: raw(t, map[string]any{
				"zone": map[string]any{"id": id},
			})}
			err := validator.validateZoneStatus(ctx, zone)
			if err == nil || !strings.Contains(err.Error(), "status.provider.data") {
				t.Fatalf("validateZoneStatus(%q) error = %v, want Cloudflare zone.id schema failure", id, err)
			}
		}
		zone := validZone()
		zone.Spec.Provider = cloudflarev1alpha1.ProviderRef
		zone.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: raw(t, map[string]any{
			"zone": map[string]any{"id": validID},
		})}
		if err := validator.validateZoneStatus(ctx, zone); err != nil {
			t.Fatalf("validateZoneStatus returned error for valid Cloudflare zone.id: %v", err)
		}
	})

	t.Run("recordset status data", func(t *testing.T) {
		cases := map[string][]map[string]any{
			"uppercase": {{"id": "023e105f4ecef8ad9ca31a8372d0C353"}},
			"non hex":   {{"id": "023e105f4ecef8ad9ca31a8372d0c35z"}},
			"short":     {{"id": "023e105f4ecef8ad9ca31a8372d0c35"}},
			"long":      {{"id": "023e105f4ecef8ad9ca31a8372d0c3530"}},
			"duplicate": {{"id": validID}, {"id": validID}},
		}
		for name, records := range cases {
			t.Run(name, func(t *testing.T) {
				recordSet := validARecordSet()
				recordSet.Spec.Provider = cloudflarev1alpha1.ProviderRef
				recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: raw(t, map[string]any{"records": records})}
				err := validator.validateRecordSetStatus(ctx, recordSet)
				if err == nil || !strings.Contains(err.Error(), "status.provider.data") {
					t.Fatalf("validateRecordSetStatus error = %v, want Cloudflare records schema failure", err)
				}
			})
		}
		recordSet := validARecordSet()
		recordSet.Spec.Provider = cloudflarev1alpha1.ProviderRef
		recordSet.Status.Provider = &dnsv1alpha1.ProviderStatus{Data: raw(t, map[string]any{
			"records": []map[string]any{{"id": validID}},
		})}
		if err := validator.validateRecordSetStatus(ctx, recordSet); err != nil {
			t.Fatalf("validateRecordSetStatus returned error for valid Cloudflare records: %v", err)
		}
	})
}

func TestZoneClassValidationAppliesExtensionCELRules(t *testing.T) {
	ctx := context.Background()
	extension := route53Provider(t)
	extension.Spec.Versions[0].ZoneClass.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
		{
			Rule:    "self.spec.identityRef.name == 'expected'",
			Message: "identityRef.name must be expected",
		},
	}
	zoneClass := route53ZoneClass(t)
	validator := newTestValidator(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}}, extension, zoneClass)

	err := validator.validateZoneClass(ctx, zoneClass)
	if err == nil || !strings.Contains(err.Error(), "identityRef.name must be expected") {
		t.Fatalf("validateZoneClass error = %v, want extension CEL failure", err)
	}

	zoneClass.Spec.IdentityRef.Name = "expected"
	if err := validator.validateZoneClass(ctx, zoneClass); err != nil {
		t.Fatalf("validateZoneClass returned error: %v", err)
	}
}

func TestProviderValidationChecksSchemaAndCELSyntax(t *testing.T) {
	extension := route53Provider(t)
	extension.Spec.Versions[0].RecordSet.SupportedTypes = nil
	err := validateProvider(extension)
	if err == nil || !strings.Contains(err.Error(), "recordSet.supportedTypes must not be empty") {
		t.Fatalf("validateProvider error = %v, want required supportedTypes failure", err)
	}

	extension = route53Provider(t)
	extension.Spec.Versions[0].RecordSet.SupportedTypes = []dnsv1alpha1.RecordType{dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeA}
	err = validateProvider(extension)
	if err == nil || !strings.Contains(err.Error(), "recordSet.supportedTypes[1] duplicates") {
		t.Fatalf("validateProvider error = %v, want duplicate supportedTypes failure", err)
	}

	extension = route53Provider(t)
	extension.Spec.Versions[0].RecordSet.SupportedTypes = append(extension.Spec.Versions[0].RecordSet.SupportedTypes, dnsv1alpha1.RecordType("HTTPS"))
	err = validateProvider(extension)
	if err == nil || !strings.Contains(err.Error(), "recordSet.supportedTypes[7] is not a known DNS record type") {
		t.Fatalf("validateProvider error = %v, want unsupported supportedTypes failure", err)
	}

	extension = route53Provider(t)
	extension.Spec.Versions[0].RecordSet.Schemas.Options = &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
		"type": []any{"object"},
	})}

	err = validateProvider(extension)
	if err == nil || !strings.Contains(err.Error(), "recordSet.schemas.options.openAPIV3Schema") {
		t.Fatalf("validateProvider error = %v, want schema failure", err)
	}

	extension = route53Provider(t)
	extension.Spec.Versions[0].RecordSet.DisableValidations[0].When = "has("
	err = validateProvider(extension)
	if err == nil || !strings.Contains(err.Error(), "recordSet.disableValidations[0].when") {
		t.Fatalf("validateProvider error = %v, want CEL failure", err)
	}

	cases := map[string]struct {
		mutate func(*dnsv1alpha1.Provider)
		path   string
	}{
		"zoneClass validation rule": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].ZoneClass.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
					{Rule: "self.spec.identityRef.name"},
				}
			},
			path: "zoneClass.validationRules[0].rule",
		},
		"zone validation rule": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
					{Rule: "self.spec.domainName"},
				}
			},
			path: "zone.validationRules[0].rule",
		},
		"recordSet validation rule": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
					{Rule: "self.spec.type"},
				}
			},
			path: "recordSet.validationRules[0].rule",
		},
		"recordSet disable validation": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.DisableValidations[0].When = "self.spec.type"
			},
			path: "recordSet.disableValidations[0].when",
		},
		"zone conversion bool": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.Conversion.ToStorage.CEL = "true"
			},
			path: "zone.conversion.toStorage.cel",
		},
		"zone conversion number": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.Conversion.ToStorage.CEL = "1"
			},
			path: "zone.conversion.toStorage.cel",
		},
		"recordSet conversion string": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.CEL = "'not an object'"
			},
			path: "recordSet.conversion.toStorage.cel",
		},
		"recordSet conversion list": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.CEL = "[]"
			},
			path: "recordSet.conversion.toStorage.cel",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			extension := route53Provider(t)
			tc.mutate(extension)

			err := validateProvider(extension)
			if err == nil {
				t.Fatalf("validateProvider returned nil, want non-bool CEL failure")
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("validateProvider error = %v, want path %q", err, tc.path)
			}
			if !strings.Contains(err.Error(), "must return bool") && !strings.Contains(err.Error(), "must return object") {
				t.Fatalf("validateProvider error = %v, want return type requirement", err)
			}
		})
	}
}

func TestProviderValidationUsesTargetSpecificCELEnvironments(t *testing.T) {
	t.Run("object validation rules cannot use composition variables", func(t *testing.T) {
		provider := route53Provider(t)
		provider.Spec.Versions[0].Zone.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
			{Rule: "self.spec.domainName == other.name"},
		}

		err := validateProvider(provider)
		if err == nil {
			t.Fatalf("validateProvider returned nil, want undeclared variable failure")
		}
		if !strings.Contains(err.Error(), "zone.validationRules[0].rule") {
			t.Fatalf("validateProvider error = %v, want zone validation path", err)
		}
		if !strings.Contains(err.Error(), "undeclared reference to 'other'") {
			t.Fatalf("validateProvider error = %v, want other to be unavailable", err)
		}
	})

	t.Run("zoneUnit validation rules can use composition variables", func(t *testing.T) {
		provider := route53Provider(t)
		provider.Spec.Versions[0].ZoneUnit.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
			{Rule: "self.type != other.type || zone.domainName == zone.domainName && provider.name == provider.name && toVersion == toVersion"},
		}

		if err := validateProvider(provider); err != nil {
			t.Fatalf("validateProvider returned error: %v", err)
		}
	})
}

func TestProviderValidationCompilesKROCELLibraries(t *testing.T) {
	provider := route53Provider(t)
	provider.Spec.Versions[0].Zone.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
		{Rule: `json.unmarshal('{"enabled":true}').enabled == true`},
	}
	provider.Spec.Versions[0].RecordSet.ValidationRules = []dnsv1alpha1.ProviderValidationRule{
		{Rule: `self.spec.options.merge({"checked": true}).checked == true`},
	}
	provider.Spec.Versions[0].ZoneUnit.DisableValidations = append(provider.Spec.Versions[0].ZoneUnit.DisableValidations, dnsv1alpha1.ZoneUnitValidationToggle{
		Name: "forbid-cname-coexistence",
		When: `lists.setAtIndex(["CNAME", "TXT"], 1, "MX")[0] == self.type ||
			base64.encode(hash.fnv64a(other.type)) != ""`,
	})
	provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.CEL = `{
		"options": {"drop": omit()},
		"adoption": omit()
	}`

	if err := validateProvider(provider); err != nil {
		t.Fatalf("validateProvider returned error: %v", err)
	}
}

func TestProviderValidationChecksConversionWebhookConfig(t *testing.T) {
	validCABundle := []byte(`-----BEGIN CERTIFICATE-----
MIIBcjCCARmgAwIBAgIUTWd2v7XcSnsnAZ09hMsN8VFe8tswCgYIKoZIzj0EAwIw
EjEQMA4GA1UEAwwHZG5zLWFwaTAeFw0yNjA2MTAwMDAwMDBaFw0yNzA2MTAwMDAw
MDBaMBIxEDAOBgNVBAMMB2Rucy1hcGkwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNC
AASoJ+uU4gI6etmNn1Aq2aKX75rTufP5I/F1Vyn6v5l+lpZlWi0v5+3LyTq1VRrN
f2DEBGfExK9+3Sm7WYwrwk8Yo1MwUTAdBgNVHQ4EFgQU+iX2b6T/6Y/aJGBDOxPE
BLAvhXAwHwYDVR0jBBgwFoAU+iX2b6T/6Y/aJGBDOxPEBLAvhXAwDwYDVR0TAQH/
BAUwAwEB/zAKBggqhkjOPQQDAgNIADBFAiEAy8pgU0zTr5UODIA83xnEHNtXA2fn
fHHHyhgPTPv/qR0CIBsSk5EuNYAdk+2Az+MtUaeUzH3NGtqmp3VXR7ugFaY1
-----END CERTIFICATE-----`)

	webhook := func() *dnsv1alpha1.ProviderConversionWebhook {
		timeout := int32(10)
		port := int32(443)
		return &dnsv1alpha1.ProviderConversionWebhook{
			ClientConfig: dnsv1alpha1.ProviderConversionWebhookClientConfig{
				Service: dnsv1alpha1.ProviderConversionWebhookServiceReference{
					Namespace: "dns-api-system",
					Name:      "provider-converter",
					Path:      "/convert",
					Port:      &port,
				},
				CABundle: validCABundle,
			},
			TimeoutSeconds: &timeout,
		}
	}

	cases := map[string]struct {
		mutate func(*dnsv1alpha1.Provider)
		want   string
	}{
		"valid webhook": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.Webhook = webhook()
			},
		},
		"cel and webhook": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.CEL = "{}"
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.Webhook = webhook()
			},
			want: "recordSet.conversion.toStorage must not combine cel and webhook",
		},
		"missing service namespace": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				hook := webhook()
				hook.ClientConfig.Service.Namespace = ""
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.Webhook = hook
			},
			want: "recordSet.conversion.toStorage.webhook.clientConfig.service.namespace is required",
		},
		"missing ca bundle": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				hook := webhook()
				hook.ClientConfig.CABundle = nil
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.Webhook = hook
			},
			want: "recordSet.conversion.toStorage.webhook.clientConfig.caBundle is required",
		},
		"invalid timeout": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				hook := webhook()
				timeout := int32(31)
				hook.TimeoutSeconds = &timeout
				provider.Spec.Versions[0].RecordSet.Conversion.ToStorage.Webhook = hook
			},
			want: "recordSet.conversion.toStorage.webhook.timeoutSeconds must be in range 1..30",
		},
		"non-storage served version requires conversion": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				next := provider.Spec.Versions[0]
				next.Name = "v1beta1"
				next.Storage = false
				provider.Spec.Versions = append(provider.Spec.Versions, next)
			},
			want: "zoneClass.conversion.toStorage must define exactly one conversion strategy for a non-storage served version",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := route53Provider(t)
			tc.mutate(provider)
			err := validateProvider(provider)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateProvider error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateProvider error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProviderValidationChecksSchemaDescriptions(t *testing.T) {
	cases := map[string]struct {
		mutate func(*dnsv1alpha1.Provider)
		want   string
	}{
		"property": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.Schemas.Adoption = &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
					"type": "object",
					"properties": map[string]any{
						"zoneID": map[string]any{"type": "string"},
					},
				})}
			},
			want: "zone.schemas.adoption.openAPIV3Schema.properties.zoneID.description is required",
		},
		"array item": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].RecordSet.Schemas.Adoption = &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recordIDs": map[string]any{
							"type":        "array",
							"description": "Cloudflare DNS record IDs to adopt for this RecordSet.",
							"items":       map[string]any{"type": "string"},
						},
					},
				})}
			},
			want: "recordSet.schemas.adoption.openAPIV3Schema.properties.recordIDs.items.description is required",
		},
		"additionalProperties": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].ZoneClass.Schemas.Parameters = &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tags": map[string]any{
							"type":                 "object",
							"description":          "Additional Route 53 tags applied to hosted zones created by this ZoneClass.",
							"additionalProperties": map[string]any{"type": "string"},
						},
					},
				})}
			},
			want: "zoneClass.schemas.parameters.openAPIV3Schema.properties.tags.additionalProperties.description is required",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := route53Provider(t)
			tc.mutate(provider)

			err := validateProvider(provider)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateProvider error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuiltInProviderManifestsPassValidation(t *testing.T) {
	for _, name := range []string{"route53_extensions.yaml", "cloudflare_extensions.yaml"} {
		t.Run(name, func(t *testing.T) {
			provider := builtInProviderManifest(t, name)
			if err := validateProvider(&provider); err != nil {
				t.Fatalf("validateProvider(%s) returned error: %v", name, err)
			}
		})
	}
}

func builtInProviderManifest(t *testing.T, name string) dnsv1alpha1.Provider {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "app", "operator", "config", "provider", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var provider dnsv1alpha1.Provider
	if err := yaml.Unmarshal(data, &provider); err != nil {
		t.Fatalf("yaml.Unmarshal(%s): %v", path, err)
	}
	return provider
}

func TestProviderValidationChecksAdditionalConditionReasons(t *testing.T) {
	cases := map[string]struct {
		mutate func(*dnsv1alpha1.Provider)
		want   string
	}{
		"invalid condition type": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
					{
						ConditionType: dnsv1alpha1.ConditionType("Ready"),
						Status:        metav1.ConditionTrue,
						Reason:        "Ready",
						Description:   "Ready reason.",
					},
				}
			},
			want: "conditionType",
		},
		"invalid status": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
					{
						ConditionType: dnsv1alpha1.ConditionAccepted,
						Status:        metav1.ConditionStatus("Pending"),
						Reason:        "Pending",
						Description:   "Pending reason.",
					},
				}
			},
			want: "status",
		},
		"invalid reason format": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
					{
						ConditionType: dnsv1alpha1.ConditionAccepted,
						Status:        metav1.ConditionFalse,
						Reason:        "provider-down",
						Description:   "Provider is down.",
					},
				}
			},
			want: "reason must be CamelCase",
		},
		"missing description": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
					{
						ConditionType: dnsv1alpha1.ConditionAccepted,
						Status:        metav1.ConditionFalse,
						Reason:        "ProviderDown",
					},
				}
			},
			want: "description is required",
		},
		"duplicate core reason": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
					{
						ConditionType: dnsv1alpha1.ConditionProgrammed,
						Status:        metav1.ConditionTrue,
						Reason:        "Programmed",
						Description:   "Duplicate core reason.",
					},
				}
			},
			want: "duplicates core reason",
		},
		"duplicate provider reason": {
			mutate: func(provider *dnsv1alpha1.Provider) {
				reason := dnsv1alpha1.ProviderConditionReason{
					ConditionType: dnsv1alpha1.ConditionProgrammed,
					Status:        metav1.ConditionFalse,
					Reason:        "Route53ChangeThrottled",
					Description:   "Route 53 change is throttled.",
				}
				provider.Spec.Versions[0].RecordSet.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{reason, reason}
			},
			want: "duplicates provider reason",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := route53Provider(t)
			tc.mutate(provider)

			err := validateProvider(provider)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateProvider error = %v, want %q", err, tc.want)
			}
		})
	}

	provider := route53Provider(t)
	provider.Spec.Versions[0].Zone.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
		{
			ConditionType: dnsv1alpha1.ConditionProgrammed,
			Status:        metav1.ConditionFalse,
			Reason:        "Route53DelegationPending",
			Description:   "Route 53 has not finished delegation.",
		},
	}
	provider.Spec.Versions[0].RecordSet.AdditionalConditionReasons = []dnsv1alpha1.ProviderConditionReason{
		{
			ConditionType: dnsv1alpha1.ConditionProgrammed,
			Status:        metav1.ConditionFalse,
			Reason:        "Route53BatchDeferred",
			Description:   "Route 53 batch is deferred.",
		},
	}
	if err := validateProvider(provider); err != nil {
		t.Fatalf("validateProvider returned error: %v", err)
	}
}

func newTestValidator(t *testing.T, objects ...client.Object) *CoreValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 AddToScheme: %v", err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("dns AddToScheme: %v", err)
	}
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("cloudflare AddToScheme: %v", err)
	}
	if err := route53v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("route53 AddToScheme: %v", err)
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()
	return NewCoreValidator(k8sClient, scheme)
}

func baseObjects(t *testing.T) []client.Object {
	t.Helper()
	return append(baseObjectsWithoutZone(t), validZone())
}

func baseObjectsWithoutZone(t *testing.T) []client.Object {
	t.Helper()
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		route53Provider(t),
		route53ZoneClass(t),
	}
}

func baseObjectsWithoutProvider(t *testing.T) []client.Object {
	t.Helper()
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}},
		route53ZoneClass(t),
	}
}

func route53Provider(t *testing.T) *dnsv1alpha1.Provider {
	t.Helper()
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
					ZoneClass: dnsv1alpha1.ProviderZoneClass{
						Schemas: dnsv1alpha1.ProviderZoneClassSchemas{
							Parameters: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
								"type":       "object",
								"properties": map[string]any{},
							})},
						},
					},
					Zone: dnsv1alpha1.ProviderZone{
						Schemas: dnsv1alpha1.ProviderZoneSchemas{
							Adoption: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
								"type":     "object",
								"required": []any{"hostedZoneId"},
								"properties": map[string]any{
									"hostedZoneId": map[string]any{
										"type":        "string",
										"pattern":     "^Z[A-Z0-9]+$",
										"description": "Route 53 public hosted zone ID of the existing hosted zone to adopt.",
									},
								},
							})},
							StatusProviderData: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
								"type": "object",
								"properties": map[string]any{
									"hostedZoneID": map[string]any{
										"type":        "string",
										"pattern":     "^Z[A-Z0-9]+$",
										"description": "Route 53 hosted zone ID managed for this Zone.",
									},
								},
							})},
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
						Schemas: dnsv1alpha1.ProviderRecordSetSchemas{
							Options: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
								"type": "object",
								"properties": map[string]any{
									"alias": map[string]any{
										"type":        "object",
										"description": "Route 53 alias target for an A or AAAA RecordSet. Alias records omit TTL and standard record values.",
										"required":    []any{"dnsName", "hostedZoneID", "evaluateTargetHealth"},
										"properties": map[string]any{
											"dnsName": map[string]any{
												"type":        "string",
												"pattern":     `^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*\.$`,
												"description": "DNS name of the Route 53 alias target. Use a trailing root dot.",
											},
											"hostedZoneID": map[string]any{
												"type":        "string",
												"pattern":     "^Z[A-Z0-9]+$",
												"description": "Hosted zone ID of the alias target resource, such as an Elastic Load Balancer CanonicalHostedZoneId.",
											},
											"evaluateTargetHealth": map[string]any{
												"type":        "boolean",
												"description": "Whether Route 53 evaluates target health for this alias record.",
											},
										},
									},
								},
							})},
							StatusProviderData: &dnsv1alpha1.ProviderOpenAPISchema{OpenAPIV3Schema: raw(t, map[string]any{
								"type": "object",
								"properties": map[string]any{
									"hostedZoneID": map[string]any{
										"type":        "string",
										"pattern":     "^Z[A-Z0-9]+$",
										"description": "Route 53 hosted zone ID containing the observed record set.",
									},
									"recordName": map[string]any{
										"type":        "string",
										"pattern":     `^.+\.$`,
										"description": "Observed Route 53 record name in trailing-dot dns-api canonical form.",
									},
									"recordType": map[string]any{
										"type":        "string",
										"enum":        []any{"A", "AAAA", "TXT", "CNAME", "MX", "CAA", "NS"},
										"description": "Observed Route 53 record type.",
									},
								},
							})},
						},
						DisableValidations: []dnsv1alpha1.RecordSetValidationToggle{
							{Name: "require-ttl", When: "has(self.spec.options) && has(self.spec.options.alias)"},
							{Name: "require-a-addresses", When: "has(self.spec.options) && has(self.spec.options.alias)"},
							{Name: "require-aaaa-addresses", When: "has(self.spec.options) && has(self.spec.options.alias)"},
						},
						ValidationRules: []dnsv1alpha1.ProviderValidationRule{
							{
								Rule:    "self.spec.type == 'A' || self.spec.type == 'AAAA' || self.spec.type == 'TXT' || self.spec.type == 'CNAME' || self.spec.type == 'MX' || self.spec.type == 'CAA' || self.spec.type == 'NS'",
								Message: "route53 supports A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias)) || self.spec.type == 'A' || self.spec.type == 'AAAA'",
								Message: "route53 alias is supported only for A and AAAA records",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.ttl))",
								Message: "ttl must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.a) && has(self.spec.a.addresses))",
								Message: "a.addresses must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.aaaa) && has(self.spec.aaaa.addresses))",
								Message: "aaaa.addresses must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.txt) && has(self.spec.txt.values))",
								Message: "txt.values must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.cname) && has(self.spec.cname.target))",
								Message: "cname.target must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.mx) && has(self.spec.mx.records))",
								Message: "mx.records must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.caa) && has(self.spec.caa.records))",
								Message: "caa.records must not be specified when route53 alias is used",
							},
							{
								Rule:    "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.ns) && has(self.spec.ns.nameServers))",
								Message: "ns.nameServers must not be specified when route53 alias is used",
							},
						},
					},
				},
			},
		},
	}
}

func route53ZoneClass(t *testing.T) *dnsv1alpha1.ZoneClass {
	t.Helper()
	return &dnsv1alpha1.ZoneClass{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "route53-public"},
		Spec: dnsv1alpha1.ZoneClassSpec{
			Provider:       route53v1alpha1.ProviderRef,
			ControllerName: "route53.dns.appthrust.io/controller",
			IdentityRef:    dnsv1alpha1.LocalObjectReference{Name: "route53-dev"},
			Parameters:     raw(t, map[string]any{}),
			AllowedZones: dnsv1alpha1.ZoneClassAllowedZones{
				Namespaces: dnsv1alpha1.NamespacePolicy{From: dnsv1alpha1.NamespacesFromSame},
			},
		},
	}
}

func validZone() *dnsv1alpha1.Zone {
	return &dnsv1alpha1.Zone{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneSpec{
			DomainName:   "apps.example.com",
			ZoneClassRef: dnsv1alpha1.ZoneClassReference{Name: "route53-public"},
			Provider:     route53v1alpha1.ProviderRef,
		},
	}
}

func validZoneUnit() *dnsv1alpha1.ZoneUnit {
	return &dnsv1alpha1.ZoneUnit{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "apps-example-com"},
		Spec: dnsv1alpha1.ZoneUnitSpec{
			Provider: route53v1alpha1.ProviderRef,
			Zone: dnsv1alpha1.ZoneUnitZoneSpec{
				Ref:                dnsv1alpha1.ObjectReference{Namespace: "app", Name: "apps-example-com"},
				ObservedGeneration: 1,
				DomainName:         "apps.example.com",
				ZoneClassRef:       dnsv1alpha1.ObjectReference{Namespace: "app", Name: "route53-public"},
			},
		},
	}
}

func validZoneUnitWithRecordSet() *dnsv1alpha1.ZoneUnit {
	zoneUnit := validZoneUnit()
	zoneUnit.Spec.RecordSets = []dnsv1alpha1.ZoneUnitRecordSetSpec{
		{
			RecordSetNamespace: "app",
			RecordSetName:      "www",
			ObservedGeneration: 1,
			Name:               "www",
			Type:               dnsv1alpha1.RecordTypeA,
			TTL:                ptr(int32(300)),
			A:                  &dnsv1alpha1.ARecordSet{Addresses: []string{"192.0.2.10"}},
		},
	}
	return zoneUnit
}

func zoneUnitUpdateRequest(t *testing.T, zoneUnit, oldZoneUnit *dnsv1alpha1.ZoneUnit) admission.Request {
	t.Helper()
	data, err := json.Marshal(zoneUnit)
	if err != nil {
		t.Fatalf("json marshal ZoneUnit: %v", err)
	}
	oldData, err := json.Marshal(oldZoneUnit)
	if err != nil {
		t.Fatalf("json marshal old ZoneUnit: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Kind: metav1.GroupVersionKind{
				Group:   dnsv1alpha1.SchemeGroupVersion.Group,
				Version: dnsv1alpha1.SchemeGroupVersion.Version,
				Kind:    "ZoneUnit",
			},
			Object:    runtime.RawExtension{Raw: data},
			OldObject: runtime.RawExtension{Raw: oldData},
		},
	}
}

func zoneUnitDeleteRequest(t *testing.T, zoneUnit *dnsv1alpha1.ZoneUnit) admission.Request {
	t.Helper()
	data, err := json.Marshal(zoneUnit)
	if err != nil {
		t.Fatalf("json marshal ZoneUnit: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Delete,
			Kind: metav1.GroupVersionKind{
				Group:   dnsv1alpha1.SchemeGroupVersion.Group,
				Version: dnsv1alpha1.SchemeGroupVersion.Version,
				Kind:    "ZoneUnit",
			},
			OldObject: runtime.RawExtension{Raw: data},
		},
	}
}

func recordSetStatusRequest(t *testing.T, zoneStatus map[string]any) admission.Request {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"apiVersion": "dns.appthrust.io/v1alpha1",
		"kind":       "RecordSet",
		"metadata": map[string]any{
			"namespace":  "app",
			"name":       "www",
			"generation": 1,
		},
		"spec": map[string]any{
			"zoneRef":  map[string]any{"name": "apps-example-com"},
			"provider": map[string]any{"name": "route53.dns.appthrust.io", "version": "v1alpha1"},
			"type":     "A",
			"name":     "www",
			"ttl":      300,
			"a":        map[string]any{"addresses": []any{"192.0.2.10"}},
		},
		"status": map[string]any{
			"observedGeneration": 1,
			"zone":               zoneStatus,
		},
	})
	if err != nil {
		t.Fatalf("json marshal RecordSet: %v", err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation:   admissionv1.Update,
			SubResource: "status",
			Kind: metav1.GroupVersionKind{
				Group:   dnsv1alpha1.SchemeGroupVersion.Group,
				Version: dnsv1alpha1.SchemeGroupVersion.Version,
				Kind:    "RecordSet",
			},
			Object: runtime.RawExtension{Raw: data},
		},
	}
}

func validARecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "www"},
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

func validAAAARecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "www-v6"},
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

func validTXTRecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "acme-challenge"},
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

func validCNAMERecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "www-cname"},
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

func validMXRecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "mail"},
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

func validCAARecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "ca-policy"},
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

func validNSRecordSet() *dnsv1alpha1.RecordSet {
	return &dnsv1alpha1.RecordSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "team-a-delegation"},
		Spec: dnsv1alpha1.RecordSetSpec{
			ZoneRef:  dnsv1alpha1.ZoneReference{Name: "apps-example-com"},
			Provider: route53v1alpha1.ProviderRef,
			Type:     dnsv1alpha1.RecordTypeNS,
			Name:     "team-a",
			TTL:      ptr(int32(300)),
			NS: &dnsv1alpha1.NSRecordSet{
				NameServers: []string{"ns-111.example-dns.net", "ns-222.example-dns.net"},
			},
		},
	}
}

func raw(t *testing.T, value any) runtime.RawExtension {
	t.Helper()
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return runtime.RawExtension{Raw: bytes}
}

func ptr[T any](value T) *T {
	return &value
}
