package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type Route53ChangeStatus string

const (
	Route53ChangeStatusPending Route53ChangeStatus = "PENDING"
	Route53ChangeStatusInSync  Route53ChangeStatus = "INSYNC"
)

type Route53ZoneStatusData struct {
	// HostedZoneID is the normalized Route 53 hosted zone ID in Z... form.
	HostedZoneID string `json:"hostedZoneID,omitempty"`

	// CallerReference is the value passed to CreateHostedZone.
	CallerReference string `json:"callerReference,omitempty"`

	// PendingHostedZoneChange contains an unfinished hosted zone change.
	PendingHostedZoneChange *Route53PendingChange `json:"pendingHostedZoneChange,omitempty"`

	// PendingRecordSetChange contains an unfinished record set batch change.
	PendingRecordSetChange *Route53PendingRecordSetChange `json:"pendingRecordSetChange,omitempty"`
}

type Route53Change struct {
	// ID is the Route 53 change ID.
	ID string `json:"id,omitempty"`

	// Status is the Route 53 change status.
	Status Route53ChangeStatus `json:"status,omitempty"`

	// SubmittedAt is the time when the Route 53 change was submitted.
	SubmittedAt metav1.Time `json:"submittedAt,omitempty"`
}

type Route53PendingChange struct {
	// ID is the Route 53 change ID.
	ID string `json:"id,omitempty"`

	// Status is the Route 53 change status.
	Status Route53ChangeStatus `json:"status,omitempty"`

	// Operation describes the dns-api operation that submitted the change.
	Operation string `json:"operation,omitempty"`

	// SubmittedAt is the time when the Route 53 change was submitted.
	SubmittedAt metav1.Time `json:"submittedAt,omitempty"`
}

type Route53PendingRecordSetChange struct {
	// ID is the Route 53 change ID.
	ID string `json:"id,omitempty"`

	// Status is the Route 53 change status.
	Status Route53ChangeStatus `json:"status,omitempty"`

	// Operation describes the dns-api operation that submitted the batch.
	Operation string `json:"operation,omitempty"`

	// SubmittedAt is the time when the Route 53 change was submitted.
	SubmittedAt metav1.Time `json:"submittedAt,omitempty"`

	// AffectedRecordSets lists RecordSets included in the batch.
	// +optional
	AffectedRecordSets []Route53AffectedRecordSet `json:"affectedRecordSets,omitempty"`
}

type Route53AffectedRecordSet struct {
	// Namespace is the RecordSet namespace.
	Namespace string `json:"namespace"`

	// Name is the RecordSet name.
	Name string `json:"name"`
}
