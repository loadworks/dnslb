package main

import (
	"context"
	"flag"
	"os"
	"reflect"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	nodeAddressAnnotation = "dnslb.loadworks.com/address"
)

var log = logf.Log.WithName("dnslb")

func main() {
	sync := flag.Int64("sync", 0, "forced reconciliation interval in seconds, 0 to disable")
	flag.Parse()

	logf.SetLogger(zap.Logger(true))
	log.Info("starting")

	duration := time.Duration(*sync) * time.Second
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		SyncPeriod: &duration,
	})

	if err != nil {
		log.Error(err, "could not create manager")
		os.Exit(1)
	}

	idx := mgr.GetFieldIndexer()
	idx.IndexField(&corev1.Service{}, "spec.type", func(o runtime.Object) []string {
		return []string{string(o.(*corev1.Service).Spec.Type)}
	})
	idx.IndexField(&corev1.Pod{}, "status.phase", func(o runtime.Object) []string {
		return []string{string(o.(*corev1.Pod).Status.Phase)}
	})

	err = builder.
		ControllerManagedBy(mgr).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return isRelevant(e.Meta, e.Object)
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return isRelevant(e.Meta, e.Object)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				if isRelevant(e.MetaOld, e.ObjectOld) || isRelevant(e.MetaNew, e.ObjectNew) {
					// select only the relevant changes
					switch old := e.ObjectOld.(type) {
					case *corev1.Service:
						new := e.ObjectNew.(*corev1.Service)
						return old.Spec.Type != new.Spec.Type || !reflect.DeepEqual(old.Spec.Selector, new.Spec.Selector)
					case *corev1.Pod:
						new := e.ObjectNew.(*corev1.Pod)
						return old.Status.Phase != new.Status.Phase || !reflect.DeepEqual(old.Labels, new.Labels)
					case *corev1.Node:
						new := e.ObjectNew.(*corev1.Node)
						return old.Annotations[nodeAddressAnnotation] != new.Annotations[nodeAddressAnnotation]
					default:
						return true
					}
				}
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return isRelevant(e.Meta, e.Object)
			},
		}).
		For(&corev1.Service{}).
		Watches(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(a handler.MapObject) []reconcile.Request {
				pod := a.Object.(*corev1.Pod)
				log.Info("pod watch", "name", pod.Name)
				reqs := []reconcile.Request{}
				// get LB services from the pod's namespace
				svcs := &corev1.ServiceList{}
				err = mgr.GetClient().List(
					context.TODO(),
					svcs,
					client.InNamespace(pod.Namespace),
					client.MatchingFields{"spec.type": string(corev1.ServiceTypeLoadBalancer)},
				)
				if err != nil {
					panic(err)
				}
				// reconcile each service matching this pod
			next:
				for _, svc := range svcs.Items {
					if len(svc.Spec.Selector) == 0 {
						continue
					}
					for label, value := range svc.Spec.Selector {
						if pod.Labels[label] != value {
							continue next
						}
					}
					reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: svc.Namespace,
						Name:      svc.Name,
					}})
				}
				return reqs
			}),
		}).
		Watches(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func(a handler.MapObject) []reconcile.Request {
				node := a.Object.(*corev1.Node)
				log.Info("node watch", "name", node.Name)
				reqs := []reconcile.Request{}
				// get all LB services
				svcs := &corev1.ServiceList{}
				err = mgr.GetClient().List(
					context.TODO(),
					svcs,
					client.MatchingFields{"spec.type": string(corev1.ServiceTypeLoadBalancer)},
				)
				if err != nil {
					panic(err)
				}
				// reconcile each service
				for _, svc := range svcs.Items {
					reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
						Namespace: svc.Namespace,
						Name:      svc.Name,
					}})
				}
				return reqs
			}),
		}).
		Complete(&ServiceReconciler{})
	if err != nil {
		log.Error(err, "could not create controller")
		os.Exit(1)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "could not start manager")
		os.Exit(1)
	}
}

// isRelevant checks if the object should be watched/reconciled
func isRelevant(meta metav1.Object, object runtime.Object) bool {
	switch obj := object.(type) {
	case *corev1.Service:
		return obj.Spec.Type == corev1.ServiceTypeLoadBalancer
	case *corev1.Pod:
		return obj.Status.Phase == corev1.PodRunning
	case *corev1.Node:
		return obj.Annotations[nodeAddressAnnotation] != ""
	default:
		return false
	}
}

// ServiceReconciler ...
type ServiceReconciler struct {
	client.Client
}

// Reconcile updates the status of Service objects
func (a *ServiceReconciler) Reconcile(req reconcile.Request) (reconcile.Result, error) {
	// get the service
	svc := &corev1.Service{}
	err := a.Get(context.TODO(), req.NamespacedName, svc)
	if err != nil {
		return reconcile.Result{}, err
	}
	log.Info("reconciling service", "name", svc.Name, "type", svc.Spec.Type)
	svc.Status.LoadBalancer = corev1.LoadBalancerStatus{}

	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		// list the relevant pods
		pods := &corev1.PodList{}
		err = a.List(
			context.TODO(),
			pods,
			client.InNamespace(req.Namespace),
			client.MatchingLabels(svc.Spec.Selector),
			client.MatchingFields{"status.phase": string(corev1.PodRunning)},
		)
		if err != nil {
			return reconcile.Result{}, err
		}

		// gather the pod node-IP mapping
		ips := make(map[string]string)
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != "" {
				ips[pod.Spec.NodeName] = pod.Status.HostIP
			}
		}

		// get all nodes
		nodes := &corev1.NodeList{}
		err = a.List(context.TODO(), nodes)
		if err != nil {
			return reconcile.Result{}, err
		}

		// build the IP list
		var ings []corev1.LoadBalancerIngress
		for _, node := range nodes.Items {
			ip := ips[node.Name]
			if ip != "" {
				addr := node.Annotations[nodeAddressAnnotation]
				if addr != "" {
					ip = addr
				}
				ings = append(ings, corev1.LoadBalancerIngress{IP: ip})
			}
		}

		svc.Status.LoadBalancer.Ingress = ings
	}

	log.Info("updating service", "status", svc.Status)
	err = a.Status().Update(context.TODO(), svc)
	return reconcile.Result{}, err
}

// InjectClient ...
func (a *ServiceReconciler) InjectClient(c client.Client) error {
	a.Client = c
	return nil
}
