/*
Copyright 2026.

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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	demov1alpha1 "github.com/Developer-dreamer/manageddatabase-operator.git/api/v1alpha1"
)

// ManagedDatabaseReconciler reconciles a ManagedDatabase object
type ManagedDatabaseReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ProvisionerURL string
}

// +kubebuilder:rbac:groups=demo.example.com,resources=manageddatabases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=demo.example.com,resources=manageddatabases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=demo.example.com,resources=manageddatabases/finalizers,verbs=update

func (r *ManagedDatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Fetch the Custom Resource from the cluster
	var dbCr demov1alpha1.ManagedDatabase
	if err := r.Get(ctx, req.NamespacedName, &dbCr); err != nil {
		// If not found, it was deleted; ignore to stop reconciliation
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return r.createDatabase(ctx, dbCr)
}

func (r *ManagedDatabaseReconciler) createDatabase(ctx context.Context, dbCr demov1alpha1.ManagedDatabase) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	payload := map[string]any{
		"name":   dbCr.Name,
		"engine": dbCr.Spec.Engine,
		"sizeGB": dbCr.Spec.SizeGB,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Error(err, "Failed to marshal payload")
		return ctrl.Result{}, err
	}

	url := fmt.Sprintf("%s/databases", r.ProvisionerURL)
	resp, err := http.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		logger.Error(err, "HTTP request failed")
		return ctrl.Result{}, err // Controller-runtime will re-queue with backoff
	}
	defer func() {
		// Draining leftovers to reuse TCP socket from idle connections
		_, _ = io.Copy(io.Discard, resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Error(closeErr, "failed to close response body")
			if err == nil {
				err = closeErr
			}
		}
	}()

	switch resp.StatusCode {
	case http.StatusCreated:
		{
			var apiResp struct {
				ID    string `json:"id"`
				State string `json:"state"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
				return ctrl.Result{}, err
			}

			dbCr.Status.DatabaseID = apiResp.ID
			dbCr.Status.State = apiResp.State
			if err := r.Status().Update(ctx, &dbCr); err != nil {
				logger.Error(err, "Failed to update ManagedDatabase status")
				return ctrl.Result{}, err
			}

			logger.Info("Successfully created database on remote. Waiting for READY/FAILED...", "id", apiResp.ID, "state", apiResp.State)
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
	case http.StatusBadRequest:
		logger.Info("Failed to create Database. Double check requested fields and try again.")
		return ctrl.Result{}, nil
	case http.StatusServiceUnavailable:
		logger.Info("Service unavailable. Starting Reconciliation.")
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	case http.StatusInternalServerError:
		// Since I can't programmatically resolve this issue, the only way is to force
		// kubernetes track state as FAILED and dropping this manifest.
		logger.Info("Remote server encountered internal error. Unable to ensure that database isn't created. Saving current request with status failed.")
		dbCr.Status.State = "ORPHANED_RESPONSE_LOST"
		if err := r.Status().Update(ctx, &dbCr); err != nil {
			logger.Error(err, "Failed to update ManagedDatabase status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown error", "status", resp.StatusCode)
		return ctrl.Result{}, fmt.Errorf("unexpected status from API: %d", resp.StatusCode)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&demov1alpha1.ManagedDatabase{}).
		Named("manageddatabase").
		Complete(r)
}
