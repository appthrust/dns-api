package route53

import (
	"context"
	"errors"
	"fmt"
	"strings"

	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type AWSConfigLoader func(ctx context.Context, region string) (aws.Config, error)

type STSClient interface {
	stscreds.AssumeRoleAPIClient
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type STSClientFactory func(config aws.Config) STSClient

// AWSProviderFactory builds a Route 53 provider from a Route53Identity.
type AWSProviderFactory struct {
	LoadConfig   AWSConfigLoader
	NewSTSClient STSClientFactory
}

func NewAWSProviderFactory() *AWSProviderFactory {
	return &AWSProviderFactory{}
}

func (f *AWSProviderFactory) ProviderForIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (Provider, error) {
	cfg, _, err := resolveAWSIdentityConfig(ctx, f.configLoader(), f.stsClientFactory(), identity)
	if err != nil {
		var identityErr *identityReasonError
		if errors.As(err, &identityErr) {
			return nil, &providerReasonError{reason: "ProviderIdentityNotReady", message: "referenced Route53Identity is not Ready"}
		}
		return nil, err
	}

	return NewAWSProvider(NewRoute53Client(cfg)), nil
}

// AWSIdentityResolver resolves and verifies Route53Identity resources through STS.
type AWSIdentityResolver struct {
	LoadConfig   AWSConfigLoader
	NewSTSClient STSClientFactory
}

func NewAWSIdentityResolver() *AWSIdentityResolver {
	return &AWSIdentityResolver{}
}

func (r *AWSIdentityResolver) ResolveIdentity(ctx context.Context, identity *route53v1alpha1.Route53Identity) (IdentityResolution, error) {
	_, resolution, err := resolveAWSIdentityConfig(ctx, r.configLoader(), r.stsClientFactory(), identity)
	return resolution, err
}

func loadAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
}

func defaultSTSClient(config aws.Config) STSClient {
	return sts.NewFromConfig(config)
}

func (f *AWSProviderFactory) configLoader() AWSConfigLoader {
	if f != nil && f.LoadConfig != nil {
		return f.LoadConfig
	}
	return loadAWSConfig
}

func (f *AWSProviderFactory) stsClientFactory() STSClientFactory {
	if f != nil && f.NewSTSClient != nil {
		return f.NewSTSClient
	}
	return defaultSTSClient
}

func (r *AWSIdentityResolver) configLoader() AWSConfigLoader {
	if r != nil && r.LoadConfig != nil {
		return r.LoadConfig
	}
	return loadAWSConfig
}

func (r *AWSIdentityResolver) stsClientFactory() STSClientFactory {
	if r != nil && r.NewSTSClient != nil {
		return r.NewSTSClient
	}
	return defaultSTSClient
}

func resolveAWSIdentityConfig(ctx context.Context, loadConfig AWSConfigLoader, newSTSClient STSClientFactory, identity *route53v1alpha1.Route53Identity) (aws.Config, IdentityResolution, error) {
	if identity == nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "CredentialUnavailable", message: "Route53Identity is required"}
	}
	if err := validateAWSRegionEndpoint(ctx, identity.Spec.Region); err != nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "InvalidRegion", message: err.Error()}
	}
	if identity.Spec.Credentials.Runtime == nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "CredentialUnavailable", message: "credentials.runtime must be specified"}
	}

	if loadConfig == nil {
		loadConfig = loadAWSConfig
	}
	if newSTSClient == nil {
		newSTSClient = defaultSTSClient
	}

	cfg, err := loadConfig(ctx, identity.Spec.Region)
	if err != nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{
			reason:  "CredentialUnavailable",
			message: fmt.Sprintf("AWS config could not be loaded for region %q: %s", identity.Spec.Region, awsErrorMessage(err)),
		}
	}
	cfg.Region = identity.Spec.Region
	if cfg.Credentials == nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "CredentialUnavailable", message: "AWS credentials provider is not configured"}
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "CredentialUnavailable", message: "runtime credentials are unavailable: " + awsErrorMessage(err)}
	}

	for index, assumeRole := range identity.Spec.AssumeRoleChain {
		stsClient := newSTSClient(cfg)
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsClient, assumeRole.RoleARN, func(options *stscreds.AssumeRoleOptions) {
			if assumeRole.ExternalID != "" {
				options.ExternalID = aws.String(assumeRole.ExternalID)
			}
			options.RoleSessionName = route53SessionName(identity, index, assumeRole.SessionName)
		}))
		if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
			return aws.Config{}, IdentityResolution{}, &identityReasonError{
				reason:  "AssumeRoleFailed",
				message: fmt.Sprintf("assume role step %d failed: %s", index+1, awsErrorMessage(err)),
			}
		}
	}

	caller, err := newSTSClient(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{reason: "GetCallerIdentityFailed", message: awsErrorMessage(err)}
	}
	if got := aws.ToString(caller.Account); got != identity.Spec.AccountID {
		return aws.Config{}, IdentityResolution{}, &identityReasonError{
			reason:  "AccountMismatch",
			message: fmt.Sprintf("resolved AWS account %q does not match Route53Identity accountID %q", got, identity.Spec.AccountID),
		}
	}

	return cfg, IdentityResolution{AccountID: aws.ToString(caller.Account)}, nil
}

func route53SessionName(identity *route53v1alpha1.Route53Identity, index int, configured string) string {
	if configured != "" {
		return configured
	}
	name := fmt.Sprintf("dns-api-%s-%s-%d", identity.Namespace, identity.Name, index)
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case strings.ContainsRune("+=,.@-", r):
			return r
		default:
			return '-'
		}
	}, name)
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

type providerReasonError struct {
	reason  string
	message string
}

func (e *providerReasonError) Error() string {
	return e.message
}

type identityReasonError struct {
	reason  string
	message string
}

func (e *identityReasonError) Error() string {
	return e.message
}

func awsErrorMessage(err error) string {
	_, message := providerErrorCondition(err)
	return message
}
