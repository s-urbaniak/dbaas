package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&MongoDB{})
	SchemeBuilder.Register(&MongoDBList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MongoDBSpec   `json:"spec,omitempty"`
	Status            MongoDBStatus `json:"status,omitempty"`
}

type MongoDBSpec struct {
	Type        string `json:"type,omitempty"`
	Version     string `json:"version,omitempty"`
	Credentials string `json:"credentials,omitempty"`
	Persistent  bool   `json:"persistent,omitempty"`
}

type MongoDBStatus struct {
	Phase            string             `json:"phase,omitempty"`
	Version          string             `json:"version,omitempty"`
	ConnectionString string             `json:"connectionString,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDB `json:"items"`
}
