// Copyright (c) 2021 Terminus, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"crypto/md5"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubeproberv1 "github.com/erda-project/kubeprober/apis/v1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ProbeReconciler reconciles a Probe object
type ProbeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=kubeprober.erda.cloud,resources=probes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kubeprober.erda.cloud,resources=probes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kubeprober.erda.cloud,resources=probes/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the probe closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Probe object against the actual probe state, and then
// perform operations to make the probe state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *ProbeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error

	probe := &kubeproberv1.Probe{}
	clusterList := &kubeproberv1.ClusterList{}

	if err = r.Get(ctx, req.NamespacedName, probe); err != nil {
		klog.Errorf("get probe spec [%s] error: %+v\n", req.Name, err)
		return ctrl.Result{}, err
	}
	//update probe status
	if err = r.List(ctx, clusterList); err != nil {
		klog.Errorf("list cluster error: %+v\n", err)
		return ctrl.Result{}, err
	}

	probeSpecByte, _ := json.Marshal(probe.Spec)
	probeSpecHas := fmt.Sprintf("%x", md5.Sum(probeSpecByte))
	if probe.Status.MD5 != fmt.Sprintf("%x", probeSpecHas) {
		probe.Status.MD5 = probeSpecHas
		err := r.Status().Update(ctx, probe)
		if err != nil {
			klog.Errorf("update md5 of probe status [%s] error: %+v\n", probe.Name, err)
			return ctrl.Result{}, err
		}
		//update probe of cluster attatched
		for i := range clusterList.Items {
			cluster := clusterList.Items[i]
			if IsContain(cluster.Status.AttachedProbes, probe.Name) {
				klog.Infof("update probe [%s] of cluster [%s]\n", probe.Name, cluster.Name)
				err = UpdateProbeOfCluster(&cluster, probe)
				if err != nil {
					klog.Errorf("update probe [%s] of cluster [%s] error: %+v\n", probe.Name, cluster.Name, err)
				}
			}
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller wibth the Manager.
func (r *ProbeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeproberv1.Probe{}).WithEventFilter(&ProbePredicate{}).
		Complete(r)
}

type ProbePredicate struct {
	predicate.Funcs
}

func (rl *ProbePredicate) Update(e event.UpdateEvent) bool {
	klog.Errorf("update update\n")
	oldObject := e.ObjectOld.(*kubeproberv1.Probe)
	newObject := e.ObjectNew.(*kubeproberv1.Probe)
	ns := newObject.GetNamespace()
	if ns != metav1.NamespaceDefault {
		return false
	}

	if !reflect.DeepEqual(oldObject.Spec, newObject.Spec) {
		return true
	}

	return false
}

func (rl *ProbePredicate) Create(e event.CreateEvent) bool {
	klog.Errorf("create create\n")
	ns := e.Object.GetNamespace()
	if ns != metav1.NamespaceDefault {
		return false
	}
	return true
}

func (rl *ProbePredicate) Delete(e event.DeleteEvent) bool {
	klog.Errorf("delete delete\n")
	ns := e.Object.GetNamespace()
	if ns != metav1.NamespaceDefault {
		return false
	}
	return true
}

func (rl *ProbePredicate) Generic(e event.GenericEvent) bool {
	klog.Errorf("generic generic\n")
	ns := e.Object.GetNamespace()
	if ns != metav1.NamespaceDefault {
		return false
	}
	return true
}
