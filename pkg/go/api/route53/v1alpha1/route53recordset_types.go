package v1alpha1

type Route53RecordSetOptions struct {
	Alias *Route53AliasTarget `json:"alias,omitempty"`
}

type Route53AliasTarget struct {
	DNSName              string `json:"dnsName"`
	HostedZoneID         string `json:"hostedZoneID"`
	EvaluateTargetHealth bool   `json:"evaluateTargetHealth"`
}

type Route53RecordSetStatusData struct {
	HostedZoneID string `json:"hostedZoneID,omitempty"`
	RecordName   string `json:"recordName,omitempty"`
	RecordType   string `json:"recordType,omitempty"`
}
