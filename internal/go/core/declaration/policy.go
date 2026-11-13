package declaration

import (
	"context"
	"regexp"
	"slices"

	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ZoneAllowedByClass(ctx context.Context, k8sClient client.Client, zoneClass *dnsv1alpha1.ZoneClass, zoneNamespace string) (bool, error) {
	policy := zoneClass.Spec.AllowedZones.Namespaces
	switch namespacePolicyFrom(policy) {
	case dnsv1alpha1.NamespacesFromSame:
		return zoneClass.Namespace == zoneNamespace, nil
	case dnsv1alpha1.NamespacesFromAll:
		return true, nil
	case dnsv1alpha1.NamespacesFromSelector:
		if policy.Selector == nil {
			return false, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(policy.Selector)
		if err != nil {
			return false, err
		}
		var namespace corev1.Namespace
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: zoneNamespace}, &namespace); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return selector.Matches(labels.Set(namespace.Labels)), nil
	default:
		return false, nil
	}
}

// RecordSetAllowedByZone distinguishes an observed policy denial from a failed
// namespace observation. Callers must not turn the error into an empty desired set.
func RecordSetAllowedByZone(ctx context.Context, k8sClient client.Client, recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone) (bool, string, error) {
	return recordSetAllowedByZone(ctx, k8sClient, recordSet, zone, true)
}

func recordSetAllowedByZone(ctx context.Context, k8sClient client.Client, recordSet *dnsv1alpha1.RecordSet, zone *dnsv1alpha1.Zone, allowSameNamespace bool) (bool, string, error) {
	if allowSameNamespace && recordSet.Namespace == zone.Namespace {
		return true, "", nil
	}
	namespaceMatched := false
	recordNameMatched := false
	for _, grant := range zone.Spec.AllowedRecordSets {
		allowed, err := namespaceAllowed(ctx, k8sClient, &grant.Namespaces.Selector, recordSet.Namespace)
		if err != nil {
			return false, "", err
		}
		if !allowed {
			continue
		}
		namespaceMatched = true
		for _, record := range grant.Records {
			matched, err := fullRecordNamePatternMatch(record.Name.Pattern, recordSet.Spec.Name)
			if err != nil || !matched {
				continue
			}
			recordNameMatched = true
			if slices.Contains(record.Types, recordSet.Spec.Type) {
				return true, "", nil
			}
		}
	}
	if !namespaceMatched {
		return false, "RecordSet namespace is not allowed by the referenced Zone.", nil
	}
	if !recordNameMatched {
		return false, "RecordSet name is not allowed by the referenced Zone.", nil
	}
	return false, "RecordSet type is not allowed for this RecordSet name by the referenced Zone.", nil
}

func namespaceAllowed(ctx context.Context, k8sClient client.Client, selectorSpec *metav1.LabelSelector, recordSetNamespace string) (bool, error) {
	if selectorSpec == nil || (len(selectorSpec.MatchLabels) == 0 && len(selectorSpec.MatchExpressions) == 0) {
		return false, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(selectorSpec)
	if err != nil {
		return false, nil
	}
	var namespace corev1.Namespace
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: recordSetNamespace}, &namespace); err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(namespace.Labels)), nil
}

func namespacePolicyFrom(policy dnsv1alpha1.NamespacePolicy) dnsv1alpha1.NamespacesFrom {
	if policy.From == "" {
		return dnsv1alpha1.NamespacesFromSame
	}
	return policy.From
}

func fullRecordNamePatternMatch(pattern, recordName string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	match := compiled.FindStringIndex(recordName)
	return match != nil && match[0] == 0 && match[1] == len(recordName), nil
}
