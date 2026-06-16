package route53

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsroute53 "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

var awsRegionNamePattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)

func validateAWSRegionEndpoint(ctx context.Context, region string) error {
	trimmed := strings.TrimSpace(region)
	if trimmed == "" {
		return fmt.Errorf("region is required")
	}
	if region != trimmed {
		return fmt.Errorf("region cannot start or end with spaces")
	}
	if !awsRegionNamePattern.MatchString(region) {
		return fmt.Errorf("region %q is not a valid AWS region name", region)
	}
	if _, err := sts.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, sts.EndpointParameters{Region: aws.String(region)}); err != nil {
		return fmt.Errorf("STS endpoint could not be resolved for region %q: %w", region, err)
	}
	if _, err := awsroute53.NewDefaultEndpointResolverV2().ResolveEndpoint(ctx, awsroute53.EndpointParameters{Region: aws.String(region)}); err != nil {
		return fmt.Errorf("Route 53 endpoint could not be resolved for region %q: %w", region, err)
	}
	return nil
}
