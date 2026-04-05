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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mckv1 "github.com/s-urbaniak/dbaas/api/mck/v1"
)

// MongoDBReconciler mock-reconciles mongodb.com/v1 MongoDB resources.
// It sets status.phase=Running and a Ready condition to simulate a real MCK operator.
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
	obj.Status.Phase = "Running"
	obj.Status.Version = obj.Spec.Version
	obj.Status.ConnectionString = fmt.Sprintf("mongodb://%s.%s.svc:27017", obj.Name, obj.Namespace)
	obj.Status.Conditions = []metav1.Condition{
		readyCondition("Mock MCK MongoDB is running"),
	}

	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching MongoDB status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *MongoDBReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&mckv1.MongoDB{}).Complete(r)
}

func readyCondition(message string) metav1.Condition {
	return metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now().UTC()),
	}
}
