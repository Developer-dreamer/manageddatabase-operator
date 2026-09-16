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
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	demov1alpha1 "github.com/Developer-dreamer/manageddatabase-operator.git/api/v1alpha1"
)

var _ = Describe("ManagedDatabase Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName      = "test-resource"
			resourceNamespace = "default"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}
		manageddatabase := &demov1alpha1.ManagedDatabase{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind ManagedDatabase")
			err := k8sClient.Get(ctx, typeNamespacedName, manageddatabase)
			if err != nil && errors.IsNotFound(err) {
				resource := &demov1alpha1.ManagedDatabase{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: resourceNamespace,
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &demov1alpha1.ManagedDatabase{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				// Strip finalizer so Kubernetes can permanently delete it
				resource.Finalizers = []string{}
				Expect(k8sClient.Update(ctx, resource)).To(Succeed())

				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully create a database and update status.", func() {
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/databases" {
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{ "id": "db-9c69e07d", "name": "orders", "engine": "postgres", "sizeGB": 20, "state": "PROVISIONING" }`))
					return
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer mockServer.Close()

			controllerReconciler := &ManagedDatabaseReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ProvisionerURL: mockServer.URL,
			}

			// Finalizer added and Reconcile exited early...
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// The finalizer is present. The controller executes the POST request.
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			// Assert the result of the second execution
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Second * 5))

			// Fetch the updated resource to verify Kubernetes state
			updatedResource := &demov1alpha1.ManagedDatabase{}
			err = k8sClient.Get(ctx, typeNamespacedName, updatedResource)
			Expect(err).NotTo(HaveOccurred())

			// Assert the finalizer and status were applied correctly
			Expect(updatedResource.Finalizers).To(ContainElement(finalizerName))
			Expect(updatedResource.Status.DatabaseID).To(Equal("db-9c69e07d"))
			Expect(updatedResource.Status.State).To(Equal("PROVISIONING"))
		})

		It("should create a resource and set status to lost. now further recreation.", func() {
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/databases" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer mockServer.Close()

			controllerReconciler := &ManagedDatabaseReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ProvisionerURL: mockServer.URL,
			}

			// Finalizer added and Reconcile exited early...
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// The finalizer is present. The controller executes the POST request.
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})

			// The reconciler successfully intercepts 500 error and sets required state
			Expect(err).NotTo(HaveOccurred())

			// Fetch the updated resource to verify Kubernetes state
			updatedResource := &demov1alpha1.ManagedDatabase{}
			err = k8sClient.Get(ctx, typeNamespacedName, updatedResource)
			Expect(err).NotTo(HaveOccurred())

			// Assert the finalizer and status were applied correctly
			Expect(updatedResource.Finalizers).To(ContainElement(finalizerName))
			Expect(updatedResource.Status.DatabaseID).To(Equal(""))
			Expect(updatedResource.Status.State).To(Equal("ORPHANED_RESPONSE_LOST"))
		})

		It("should successfully delete the resource.", func() {
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Expecting a DELETE request targeting the specific mock ID
				if r.Method == http.MethodDelete && r.URL.Path == "/databases/db-test-delete" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer mockServer.Close()

			controllerReconciler := &ManagedDatabaseReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				ProvisionerURL: mockServer.URL,
			}

			// Blank resource created by the BeforeEach block
			resource := &demov1alpha1.ManagedDatabase{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())

			// Append the finalizer and update cluster state
			resource.Finalizers = append(resource.Finalizers, finalizerName)
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			// Set the DatabaseID in the Status subresource
			resource.Status.DatabaseID = "db-test-delete"
			Expect(k8sClient.Status().Update(ctx, resource)).To(Succeed())

			// Issue the delete command.
			// The finalizer prevents absolute deletion and applies the DeletionTimestamp.
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Invoke Reconcile.
			// It evaluates DeletionTimestamp != Zero and routes to handleDeletion.
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Assert the resource is permanently purged from the cluster
			err = k8sClient.Get(ctx, typeNamespacedName, &demov1alpha1.ManagedDatabase{})
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})
