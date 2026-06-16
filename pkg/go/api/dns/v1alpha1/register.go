package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		SchemeGroupVersion,
		&Provider{},
		&ProviderList{},
		&ZoneClass{},
		&ZoneClassList{},
		&Zone{},
		&ZoneList{},
		&ZoneUnit{},
		&ZoneUnitList{},
		&RecordSet{},
		&RecordSetList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
