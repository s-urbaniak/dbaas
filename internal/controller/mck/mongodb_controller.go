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

package mck

import (
	"context"
	"fmt"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/v1/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mckv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

// MongoDBReconciler mock-reconciles mongodb.com/v1 MongoDB resources.
// It sets the upstream MCK phase and message fields to simulate a running cluster.
type MongoDBReconciler struct {
	client.Client
}

func (r *MongoDBReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling MongoDB", "name", req.NamespacedName)

	obj := &mckv1.MongoDB{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	patch := client.MergeFrom(obj.DeepCopy())
	obj.Status.Phase = mdbstatus.PhaseRunning
	obj.Status.Version = obj.Spec.Version
	obj.Status.Message = "Mock MCK MongoDB is running"
	obj.Status.ObservedGeneration = obj.Generation

	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching MongoDB status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *MongoDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&mckv1.MongoDB{}).Complete(r)
}
