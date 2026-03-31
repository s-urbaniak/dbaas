/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var flexClusterGVK = schema.GroupVersionKind{
	Group:   "atlas.generated.mongodb.com",
	Version: "v1",
	Kind:    "FlexCluster",
}

// FlexClusterReconciler mock-reconciles atlas.generated.mongodb.com/v1 FlexCluster resources.
// It sets the IDLE stateName and a Ready condition to simulate a real Atlas operator.
type FlexClusterReconciler struct {
	client.Client
}

func (r *FlexClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling FlexCluster", "name", req.NamespacedName)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(flexClusterGVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(obj.DeepCopy())
	_ = unstructured.SetNestedField(obj.Object, "IDLE", "status", "v20250312", "stateName")
	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		readyCondition("Mock Atlas FlexCluster is idle"),
		map[string]interface{}{
			"type":               "State",
			"status":             "True",
			"reason":             "IDLE",
			"message":            "Mock FlexCluster stateName=IDLE",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		},
	}, "status", "conditions")

	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching FlexCluster status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *FlexClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(flexClusterGVK)
	return ctrl.NewControllerManagedBy(mgr).For(u).Complete(r)
}
