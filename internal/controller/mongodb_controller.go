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

var mongoDBGVK = schema.GroupVersionKind{
	Group:   "mongodb.com",
	Version: "v1",
	Kind:    "MongoDB",
}

// MongoDBReconciler mock-reconciles mongodb.com/v1 MongoDB resources.
// It sets status.phase=Running and a Ready condition to simulate a real MCK operator.
type MongoDBReconciler struct {
	client.Client
}

func (r *MongoDBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling MongoDB", "name", req.NamespacedName)

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(mongoDBGVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	version, _, _ := unstructured.NestedString(obj.Object, "spec", "version")

	patch := client.MergeFrom(obj.DeepCopy())
	_ = unstructured.SetNestedField(obj.Object, "Running", "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, version, "status", "version")
	_ = unstructured.SetNestedSlice(obj.Object, []interface{}{
		readyCondition("Mock MCK MongoDB is running"),
	}, "status", "conditions")

	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching MongoDB status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *MongoDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(mongoDBGVK)
	return ctrl.NewControllerManagedBy(mgr).For(u).Complete(r)
}

// readyCondition returns a map representing a Kubernetes Ready=True condition.
func readyCondition(message string) map[string]interface{} {
	return map[string]interface{}{
		"type":               "Ready",
		"status":             "True",
		"reason":             "Reconciled",
		"message":            message,
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	}
}
