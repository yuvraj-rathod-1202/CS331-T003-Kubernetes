//go:build linux

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

// Command linkrouter is the per-node link router. It runs as a hostNetwork
// DaemonSet with NET_ADMIN; see internal/controller/cniresilience/linkrouter.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/cniresilience/linkrouter"
)

func main() {
	def := linkrouter.DefaultConfig()
	lc := def
	var (
		nodeName string
		interval time.Duration
		cleanup  bool
	)
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of the node this router runs on.")
	flag.IntVar(&lc.Table, "table", def.Table, "Routing table holding the steering routes.")
	flag.IntVar(&lc.Protocol, "protocol", def.Protocol, "Route protocol marking routes as owned.")
	flag.IntVar(&lc.ProbePort, "probe-port", def.ProbePort, "TCP port of the health probe, kept off the steering table.")
	flag.DurationVar(&interval, "interval", 10*time.Second,
		"Longest time between syncs; watch events trigger one sooner.")
	flag.BoolVar(&cleanup, "cleanup", false, "Delete every route and rule the router owns, then exit.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("linkrouter")

	host := linkrouter.NetlinkHost{Config: lc}
	if cleanup {
		if err := linkrouter.Cleanup(host); err != nil {
			log.Error(err, "Cleanup failed")
			os.Exit(1)
		}
		log.Info("Cleanup finished", "table", lc.Table, "protocol", lc.Protocol)
		return
	}

	if nodeName == "" {
		log.Error(nil, "Node name is required (--node-name or NODE_NAME)")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(remediationv1alpha1.AddToScheme(scheme))

	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Unable to create client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// Nodes, CNIResiliences and Calico BlockAffinities come from a watch-fed
	// cache; a change triggers a sync. If a watch breaks, the periodic sync still
	// converges.
	trigger := make(chan struct{}, 1)
	ca, err := cache.New(cfg, cache.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Unable to create cache")
		os.Exit(1)
	}
	kick := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { nudge(trigger) },
		UpdateFunc: func(_, _ any) { nudge(trigger) },
		DeleteFunc: func(any) { nudge(trigger) },
	}
	blocks := &unstructured.Unstructured{}
	blocks.SetGroupVersionKind(linkrouter.BlockAffinityGVK)
	for _, obj := range []client.Object{&corev1.Node{}, &remediationv1alpha1.CNIResilience{}, blocks} {
		inf, err := ca.GetInformer(ctx, obj)
		if err != nil {
			if obj == client.Object(blocks) {
				// The CRD may be absent; the periodic sync treats that as no blocks.
				log.Error(err, "Unable to watch BlockAffinity, continuing without it")
				continue
			}
			log.Error(err, "Unable to watch", "type", fmt.Sprintf("%T", obj))
			os.Exit(1)
		}
		if _, err := inf.AddEventHandler(kick); err != nil {
			log.Error(err, "Unable to add event handler")
			os.Exit(1)
		}
	}
	go func() {
		if err := ca.Start(ctx); err != nil {
			log.Error(err, "Cache stopped")
			os.Exit(1)
		}
	}()
	if !ca.WaitForCacheSync(ctx) {
		log.Error(nil, "Cache did not sync")
		os.Exit(1)
	}

	r := &linkrouter.Router{Reader: ca, Writer: c, NodeName: nodeName, Host: host, Config: lc}
	log.Info("Starting", "node", nodeName, "interval", interval.String())
	r.Run(ctrl.LoggerInto(ctx, log), interval, trigger)
}

// nudge asks for a sync without blocking; one pending request is enough.
func nudge(c chan<- struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
