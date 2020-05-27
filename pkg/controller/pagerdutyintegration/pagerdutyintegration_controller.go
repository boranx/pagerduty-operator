package pagerdutyintegration

import (
	"context"

	"github.com/go-logr/logr"
	hivev1 "github.com/openshift/hive/pkg/apis/hive/v1"
	"github.com/openshift/pagerduty-operator/config"
	pagerdutyv1alpha1 "github.com/openshift/pagerduty-operator/pkg/apis/pagerduty/v1alpha1"
	pd "github.com/openshift/pagerduty-operator/pkg/pagerduty"
	"github.com/openshift/pagerduty-operator/pkg/utils"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_pagerdutyintegration")

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new PagerDutyIntegration Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	newRec, err := newReconciler(mgr)
	if err != nil {
		return err
	}

	return add(mgr, newRec)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) (reconcile.Reconciler, error) {
	tempClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return nil, err
	}

	// get PD API key from secret
	pdAPIKey, err := utils.LoadSecretData(tempClient, config.PagerDutyAPISecretName, config.OperatorNamespace, config.PagerDutyAPISecretKey)

	return &ReconcilePagerDutyIntegration{
		client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		pdclient: pd.NewClient(pdAPIKey),
	}, nil
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("pagerdutyintegration-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource PagerDutyIntegration
	err = c.Watch(&source.Kind{Type: &pagerdutyv1alpha1.PagerDutyIntegration{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcilePagerDutyIntegration implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcilePagerDutyIntegration{}

// ReconcilePagerDutyIntegration reconciles a PagerDutyIntegration object
type ReconcilePagerDutyIntegration struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client    client.Client
	scheme    *runtime.Scheme
	reqLogger logr.Logger
	pdclient  pd.Client
}

// Reconcile reads that state of the cluster for a PagerDutyIntegration object and makes changes based on the state read
// and what is in the PagerDutyIntegration.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcilePagerDutyIntegration) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	r.reqLogger = log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	r.reqLogger.Info("Reconciling PagerDutyIntegration")

	// Fetch the PagerDutyIntegration instance
	pdi := &pagerdutyv1alpha1.PagerDutyIntegration{}
	err := r.client.Get(context.TODO(), request.NamespacedName, pdi)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	matchingClusterDeployments, err := r.getMatchingClusterDeployments(pdi)
	if err != nil {
		return reconcile.Result{}, err
	}

	if pdi.DeletionTimestamp != nil {
		if utils.HasFinalizer(pdi, config.OperatorFinalizer) {
			for _, cd := range matchingClusterDeployments.Items {
				_, err := r.handleDelete(pdi, &cd)
				if err != nil {
					return reconcile.Result{}, err
				}
			}

			utils.DeleteFinalizer(pdi, config.OperatorFinalizer)
			err = r.client.Update(context.TODO(), pdi)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{}, nil
	}

	if !utils.HasFinalizer(pdi, config.OperatorFinalizer) {
		utils.AddFinalizer(pdi, config.OperatorFinalizer)
		err := r.client.Update(context.TODO(), pdi)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	for _, cd := range matchingClusterDeployments.Items {
		if cd.DeletionTimestamp != nil {
			_, err := r.handleDelete(pdi, &cd)
			if err != nil {
				return reconcile.Result{}, err
			}
		} else {
			_, err := r.handleCreate(pdi, &cd)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	return reconcile.Result{}, nil
}

func (r *ReconcilePagerDutyIntegration) getMatchingClusterDeployments(pdi *pagerdutyv1alpha1.PagerDutyIntegration) (*hivev1.ClusterDeploymentList, error) {

	// TODO: not sure if this should be here or in the CRs?
	// Don't match any ClusterDeployments that have noalerts label set
	labelSelector := pdi.Spec.ClusterDeploymentSelector.DeepCopy()
	labelSelector.MatchExpressions = append(labelSelector.MatchExpressions, metav1.LabelSelectorRequirement{
		Key:      config.ClusterDeploymentNoalertsLabel,
		Operator: metav1.LabelSelectorOpNotIn,
		Values:   []string{"true"},
	})
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return nil, err
	}

	matchingClusterDeployments := &hivev1.ClusterDeploymentList{}
	listOpts := &client.ListOptions{LabelSelector: selector}
	err = r.client.List(context.TODO(), listOpts, matchingClusterDeployments)
	return matchingClusterDeployments, err
}
