package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = ProviderName

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: ProviderVersion}
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
var AddToScheme = SchemeBuilder.AddToScheme

// External has inline payload types only: no identity, Zone or RecordSet CRDs.
func addKnownTypes(scheme *runtime.Scheme) error {
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
