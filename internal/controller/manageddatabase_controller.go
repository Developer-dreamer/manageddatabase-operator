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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	demov1alpha1 "github.com/Developer-dreamer/manageddatabase-operator.git/api/v1alpha1"
)

// ManagedDatabaseReconciler reconciles a ManagedDatabase object
type ManagedDatabaseReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ProvisionerURL string
}

const finalizerName = "manageddatabase.demo.example.com/finalizer"

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

	// Check if the resource is scheduled for deletion
	if !dbCr.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &dbCr)
	}

	// Terminal failure check: never retry if the response was lost
	if dbCr.Status.State == "ORPHANED_RESPONSE_LOST" {
		logger.Info("Database creation failed with unrecoverable lost response. Manual intervention required.")
		return ctrl.Result{}, nil
	}

	// Terminal failure check: never retry if the response was lost
	if dbCr.Status.State == "ORPHANED_RESPONSE_LOST" {
		logger.Info("Database creation failed with unrecoverable lost response. Manual intervention required.")
		return ctrl.Result{}, nil
	}

	// Check if database exists and has state READY or FAILED
	if dbCr.Status.DatabaseID != "" {
		if dbCr.Status.State == "PROVISIONING" {
			// Cluster database status requires to be updated. Fetching status from operator
			return r.fetchStatus(ctx, dbCr)
		}
		logger.Info("Database already exists", "id", dbCr.Status.DatabaseID)
		return ctrl.Result{}, nil
	}

	return r.createDatabase(ctx, dbCr)
}

func (r *ManagedDatabaseReconciler) createDatabase(ctx context.Context, dbCr demov1alpha1.ManagedDatabase) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Adding finalizer to the resource. Used to lock object state when deleting
	// (kubernetes does not delete it instantly however leaves as time window to process with remote deletion)
	if !controllerutil.ContainsFinalizer(&dbCr, finalizerName) {
		controllerutil.AddFinalizer(&dbCr, finalizerName)
		if err := r.Update(ctx, &dbCr); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Added finalizer. Starting Reconciliation.")
		return ctrl.Result{RequeueAfter: 1 * time.Microsecond}, nil // Requeueing instantly
	}

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

func (r *ManagedDatabaseReconciler) fetchStatus(ctx context.Context, dbCr demov1alpha1.ManagedDatabase) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	url := fmt.Sprintf("%s/databases/%s", r.ProvisionerURL, dbCr.Status.DatabaseID)
	resp, err := http.Get(url)
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
	case http.StatusOK:
		var apiResp struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			logger.Error(err, "Failed to unmarshal response")
			return ctrl.Result{}, err
		}

		if apiResp.Endpoint == "" && apiResp.State == "PROVISIONING" {
			logger.Info("Database is still provisioned. Starting Reconciliation.", "id", apiResp.ID, "state", apiResp.State)
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}

		dbCr.Status.State = apiResp.State
		dbCr.Status.Endpoint = apiResp.Endpoint
		if err := r.Status().Update(ctx, &dbCr); err != nil {
			logger.Error(err, "Failed to update ManagedDatabase status")
			return ctrl.Result{}, err
		}

		if apiResp.State == "FAILED" {
			logger.Info("Remote failed to create db.", "id", apiResp.ID, "state", apiResp.State)
		} else {
			logger.Info("Resource created successfully.", "id", apiResp.ID, "state", apiResp.State)
		}
		return ctrl.Result{}, nil
	case http.StatusNotFound:
		logger.Info("Database with such ID does not exist.")
		return ctrl.Result{}, nil
	case http.StatusInternalServerError:
		logger.Info("Internal server error. Starting Reconciliation.")
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	default:
		logger.Info("Unknown error", "status", resp.StatusCode)
		return ctrl.Result{}, fmt.Errorf("unexpected status from API: %d", resp.StatusCode)
	}
}

func (r *ManagedDatabaseReconciler) handleDeletion(ctx context.Context, dbCr *demov1alpha1.ManagedDatabase) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(dbCr, finalizerName) {
		logger.Info("Finalizer does not exist. Can't remove resource properly.")
		return ctrl.Result{}, nil
	}

	if dbCr.Status.DatabaseID != "" {
		if err := r.deleteExternalDatabase(ctx, dbCr); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(dbCr, finalizerName)
	if err := r.Update(ctx, dbCr); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Successfully removed finalizer.", "id", dbCr.Status.DatabaseID)
	return ctrl.Result{}, nil
}

func (r *ManagedDatabaseReconciler) deleteExternalDatabase(ctx context.Context, dbCr *demov1alpha1.ManagedDatabase) error {
	logger := logf.FromContext(ctx)

	url := fmt.Sprintf("%s/databases/%s", r.ProvisionerURL, dbCr.Status.DatabaseID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		logger.Error(err, "Construct HTTP request failed")
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error(err, "HTTP request failed")
		return err // Controller-runtime will re-queue with backoff
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
	case http.StatusNoContent:
		logger.Info("Successfully deleted database on remote.")
		return nil
	case http.StatusNotFound:
		logger.Info("Database with such ID does not exist.")
		return nil
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		logger.Info("Internal server error. Starting Reconciliation.")
		return fmt.Errorf("internal server error. starting Reconciliation")
	default:
		logger.Info("Unknown error", "status", resp.StatusCode)
		return fmt.Errorf("unknown status from API: %d", resp.StatusCode)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedDatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&demov1alpha1.ManagedDatabase{}).
		Named("manageddatabase").
		Complete(r)
}
