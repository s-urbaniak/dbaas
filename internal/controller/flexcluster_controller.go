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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	atlasv1 "github.com/s-urbaniak/dbaas/api/atlas/v1"
)

// FlexClusterReconciler mock-reconciles atlas.generated.mongodb.com/v1 FlexCluster resources.
// It sets the IDLE stateName and a Ready condition to simulate a real Atlas operator.
type FlexClusterReconciler struct {
	client.Client
}

func (r *FlexClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling FlexCluster", "name", req.NamespacedName)

	obj := &atlasv1.FlexCluster{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	stateName := "IDLE"
	standardSrv := fmt.Sprintf("mongodb+srv://%s.%s.svc", obj.Name, obj.Namespace)
	patch := client.MergeFrom(obj.DeepCopy())
	obj.Status.V20250312 = &atlasv1.FlexClusterStatusV20250312{
		StateName: &stateName,
		ConnectionStrings: &atlasv1.V20250312ConnectionStrings{
			StandardSrv: &standardSrv,
		},
	}
	conditions := []metav1.Condition{
		readyCondition("Mock Atlas FlexCluster is idle"),
		{
			Type:               "State",
			Status:             metav1.ConditionTrue,
			Reason:             "IDLE",
			Message:            "Mock FlexCluster stateName=IDLE",
			LastTransitionTime: metav1.NewTime(time.Now().UTC()),
		},
	}
	obj.Status.Conditions = &conditions

	if err := r.Status().Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching FlexCluster status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *FlexClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&atlasv1.FlexCluster{}).Complete(r)
}
