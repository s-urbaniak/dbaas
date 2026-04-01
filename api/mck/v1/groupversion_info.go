package v1

import (
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	scheme "sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{
		Group:   "mongodb.com",
		Version: "v1",
	}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
