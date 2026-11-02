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

// Command ipam-agent is the per-node IPAM agent. It runs as a hostNetwork,
// privileged DaemonSet; see internal/controller/ipam/agent.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
	"CS331-CN-Project-1/operator/internal/controller/ipam/agent"
)

func main() {
	var (
		nodeName       string
		confPath       string
		netName        string
		dataDir        string
		interval       time.Duration
		promoteTimeout time.Duration
		reclaim        bool
		cleanNAT       bool
		leakMinAge     time.Duration
		drainSettle    time.Duration
	)
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of the node this agent runs on.")
	flag.StringVar(&confPath, "cni-conf", "/etc/cni/net.d/10-buddy.conflist", "CNI conflist to write.")
	flag.StringVar(&netName, "network-name", "buddy", "CNI network name.")
	flag.StringVar(&dataDir, "data-dir", "/var/lib/cni/networks", "host-local data directory.")
	flag.DurationVar(&interval, "interval", 30*time.Second,
		"Longest time between passes; watch events trigger one sooner.")
	flag.DurationVar(&promoteTimeout, "promote-timeout", 30*time.Second,
		"How long a Pending block waits for route acks before it is promoted anyway.")
	flag.BoolVar(&cleanNAT, "clean-stale-nat", true,
		"Remove nat POSTROUTING rules to CNI-* chains left by an earlier CNI plugin for addresses in our blocks.")
	flag.BoolVar(&reclaim, "reclaim-leaks", true, "Remove host-local leases that no pod on this node holds.")
	flag.DurationVar(&leakMinAge, "leak-min-age", 2*time.Minute,
		"A lease must be this old before it can be reclaimed as leaked.")
	flag.DurationVar(&drainSettle, "drain-settle", 10*time.Second,
		"How long a block stays Draining before it can be reported empty.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("ipam-agent")

	if nodeName == "" {
		log.Error(nil, "node name is required (--node-name or NODE_NAME)")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(remediationv1alpha1.AddToScheme(scheme))

	cfg := ctrl.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create client")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	// Nodes and NodeBlocks come from a watch-fed cache; a change triggers a pass.
	// If the watch breaks, the periodic pass still converges.
	trigger := make(chan struct{}, 1)
	ca, err := cache.New(cfg, cache.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create cache")
		os.Exit(1)
	}
	kick := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { nudge(trigger) },
		UpdateFunc: func(_, _ any) { nudge(trigger) },
		DeleteFunc: func(any) { nudge(trigger) },
	}
	for _, obj := range []client.Object{&corev1.Node{}, &remediationv1alpha1.NodeBlock{}} {
		inf, err := ca.GetInformer(ctx, obj)
		if err != nil {
			log.Error(err, "unable to watch", "type", fmt.Sprintf("%T", obj))
			os.Exit(1)
		}
		if _, err := inf.AddEventHandler(kick); err != nil {
			log.Error(err, "unable to add event handler")
			os.Exit(1)
		}
	}
	go func() {
		if err := ca.Start(ctx); err != nil {
			log.Error(err, "cache stopped")
			os.Exit(1)
		}
	}()
	if !ca.WaitForCacheSync(ctx) {
		log.Error(nil, "cache did not sync")
		os.Exit(1)
	}

	a := &agent.Agent{
		Client:         c,
		Reader:         ca,
		NodeName:       nodeName,
		Routes:         agent.NetlinkRoutes{},
		ConfPath:       confPath,
		NetName:        netName,
		DataDir:        dataDir,
		LeaseDir:       filepath.Join(dataDir, netName),
		PromoteTimeout: promoteTimeout,
		ReclaimLeaks:   reclaim,
		LeakMinAge:     leakMinAge,
		DrainSettle:    drainSettle,
	}

	if cleanNAT {
		a.NAT, a.NATEvery = agent.HostIPTables, time.Minute
	}
	log.Info("starting", "node", nodeName, "conf", confPath, "interval", interval.String())
	a.Run(ctrl.LoggerInto(ctx, log), interval, trigger)
}

// nudge asks for a pass without blocking; one pending request is enough.
func nudge(c chan<- struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
