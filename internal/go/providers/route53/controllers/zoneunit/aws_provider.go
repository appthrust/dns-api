package route53

import (
	"context"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsroute53 "github.com/aws/aws-sdk-go-v2/service/route53"
	awstypes "github.com/aws/aws-sdk-go-v2/service/route53/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
