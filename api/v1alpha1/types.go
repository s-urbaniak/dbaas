package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Kubernetes struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubernetesSpec   `json:"spec,omitempty"`
	Status KubernetesStatus `json:"status,omitempty"`
}

type KubernetesSpec struct {
	// AllowSchedulingOnControlPlanes allows regular pods to be scheduled
	// onto control-plane nodes. Defaults to true.
	AllowSchedulingOnControlPlanes *bool `json:"allowSchedulingOnControlPlanes,omitempty"`

	// MachineCount specifies the requested control-plane and worker node counts.
	MachineCount MachineCount `json:"machineCount"`
}

type MachineCount struct {
	// ControlPlane is the number of control-plane nodes to provision.
	// +kubebuilder:validation:Minimum=1
	ControlPlane int `json:"controlPlane"`

	// Worker is the number of worker nodes to provision.
	// +kubebuilder:validation:Minimum=0
	Worker int `json:"worker"`
}

type KubernetesStatus struct {
	Phase            string `json:"phase,omitempty"`
	Ready            bool   `json:"ready,omitempty"`
	URL              string `json:"URL,omitempty"`
	MountedWorkspace string `json:"mountedWorkspace,omitempty"`
}

// +kubebuilder:object:root=true
type KubernetesList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Kubernetes `json:"items"`
}
