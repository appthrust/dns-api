package route53

import (
	"context"
	"testing"
	"time"

	route53v1alpha1 "github.com/appthrust/dns-api/pkg/go/api/route53/v1alpha1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

func TestAWSIdentityResolverLoadsConfigWithIdentityRegion(t *testing.T) {
	ctx := context.Background()
	loadedRegion := ""
	calls := []string{}
	resolver := &AWSIdentityResolver{
		LoadConfig: func(_ context.Context, region string) (aws.Config, error) {
			loadedRegion = region
			return aws.Config{
				Region:      "us-east-1",
				Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
			}, nil
		},
		NewSTSClient: func(config aws.Config) STSClient {
			return &fakeSTSClient{region: config.Region, accountID: "123456789012", calls: &calls}
		},
	}
	identity := &route53v1alpha1.Route53Identity{
		Spec: route53v1alpha1.Route53IdentitySpec{
			AccountID: "123456789012",
			Region:    "ap-northeast-1",
			Credentials: route53v1alpha1.Route53Credentials{
				Runtime: &route53v1alpha1.Route53RuntimeCredentials{},
			},
			AssumeRoleChain: []route53v1alpha1.Route53AssumeRole{
				{RoleARN: "arn:aws:iam::123456789012:role/dns-api-route53"},
			},
		},
	}

	resolution, err := resolver.ResolveIdentity(ctx, identity)
	if err != nil {
		t.Fatalf("ResolveIdentity returned error: %v", err)
	}

	if loadedRegion != "ap-northeast-1" {
		t.Fatalf("loaded region = %q, want ap-northeast-1", loadedRegion)
	}
	if resolution.AccountID != "123456789012" {
		t.Fatalf("account ID = %q, want 123456789012", resolution.AccountID)
	}
	wantCalls := []string{"assume:ap-northeast-1", "get:ap-northeast-1"}
	if len(calls) != len(wantCalls) {
		t.Fatalf("STS calls = %#v, want %#v", calls, wantCalls)
	}
	for index := range wantCalls {
		if calls[index] != wantCalls[index] {
			t.Fatalf("STS calls = %#v, want %#v", calls, wantCalls)
		}
	}
}

type fakeSTSClient struct {
	region    string
	accountID string
	calls     *[]string
}

func (c *fakeSTSClient) AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	*c.calls = append(*c.calls, "assume:"+c.region)
	return &sts.AssumeRoleOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String("ASSUMED"),
			SecretAccessKey: aws.String("SECRET"),
			SessionToken:    aws.String("TOKEN"),
			Expiration:      aws.Time(time.Now().Add(time.Hour)),
		},
	}, nil
}

func (c *fakeSTSClient) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	*c.calls = append(*c.calls, "get:"+c.region)
	return &sts.GetCallerIdentityOutput{Account: aws.String(c.accountID)}, nil
}
