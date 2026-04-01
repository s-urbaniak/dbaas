package v1

import (
	k8s "github.com/crd2go/crd2go/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&FlexCluster{})
	SchemeBuilder.Register(&FlexClusterList{})
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

type FlexCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FlexClusterSpec   `json:"spec,omitempty"`
	Status            FlexClusterStatus `json:"status,omitempty"`
}

type FlexClusterSpec struct {
	ConnectionSecretRef *k8s.LocalReference     `json:"connectionSecretRef,omitempty"`
	V20250312           *FlexClusterSpecV20250312 `json:"v20250312,omitempty"`
}

type FlexClusterSpecV20250312 struct {
	Entry    *FlexClusterSpecV20250312Entry `json:"entry,omitempty"`
	GroupId  *string                        `json:"groupId,omitempty"`
	GroupRef *k8s.LocalReference            `json:"groupRef,omitempty"`
}

type FlexClusterSpecV20250312Entry struct {
	Name                         string          `json:"name"`
	ProviderSettings             ProviderSettings `json:"providerSettings"`
	Tags                         *[]Tags         `json:"tags,omitempty"`
	TerminationProtectionEnabled *bool           `json:"terminationProtectionEnabled,omitempty"`
}

type ProviderSettings struct {
	BackingProviderName string `json:"backingProviderName"`
	RegionName          string `json:"regionName"`
}

type FlexClusterStatus struct {
	Conditions *[]metav1.Condition        `json:"conditions,omitempty"`
	V20250312  *FlexClusterStatusV20250312 `json:"v20250312,omitempty"`
}

type FlexClusterStatusV20250312 struct {
	BackupSettings       *DiskGB                    `json:"backupSettings,omitempty"`
	ClusterType          *string                    `json:"clusterType,omitempty"`
	ConnectionStrings    *V20250312ConnectionStrings `json:"connectionStrings,omitempty"`
	CreateDate           *string                    `json:"createDate,omitempty"`
	GroupId              *string                    `json:"groupId,omitempty"`
	Id                   *string                    `json:"id,omitempty"`
	MongoDBVersion       *string                    `json:"mongoDBVersion,omitempty"`
	Name                 *string                    `json:"name,omitempty"`
	ProviderSettings     V20250312ProviderSettings  `json:"providerSettings"`
	StateName            *string                    `json:"stateName,omitempty"`
	VersionReleaseSystem *string                    `json:"versionReleaseSystem,omitempty"`
}

type V20250312ConnectionStrings struct {
	Standard    *string `json:"standard,omitempty"`
	StandardSrv *string `json:"standardSrv,omitempty"`
}

type V20250312ProviderSettings struct {
	BackingProviderName *string  `json:"backingProviderName,omitempty"`
	DiskSizeGB          *float64 `json:"diskSizeGB,omitempty"`
	ProviderName        *string  `json:"providerName,omitempty"`
	RegionName          *string  `json:"regionName,omitempty"`
}

type DiskGB struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type Tags struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// +kubebuilder:object:root=true
type FlexClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FlexCluster `json:"items"`
}
