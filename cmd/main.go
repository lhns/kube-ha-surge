// Command kube-ha-surge runs the operator: a ValidatingWebhook on pods/eviction
// that gives opted-in single-replica Deployments make-before-break HA during
// node drains, plus a controller that garbage-collects the stand-in pods.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/lhns/kube-ha-surge/internal/controller"
	"github.com/lhns/kube-ha-surge/internal/webhook/eviction"
)

// Stamped via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		metricsAddr   string
		probeAddr     string
		leaderElect   bool
		maxSurgeSecs  int
		certDir       string
		webhookPort   int
		stripPrefixes string
		showVersion   bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the metric endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the health probe binds to")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election for controller manager")
	flag.IntVar(&maxSurgeSecs, "max-surge-seconds", 120, "max time to hold an eviction waiting for a stand-in before failing open")
	flag.StringVar(&certDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs", "directory holding the webhook serving cert (tls.crt/tls.key)")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "port the webhook server binds to")
	flag.StringVar(&stripPrefixes, "strip-label-prefixes", "", "comma-separated label-key prefixes to drop from the cloned surge pod (for labels a controller injects at runtime and re-derives; some clusters guard such labels with admission policies)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if showVersion {
		fmt.Printf("kube-ha-surge %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")
	log.Info("starting kube-ha-surge", "version", version, "commit", commit, "date", date)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "add client-go scheme")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "kube-ha-surge.lhns.de",
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: certDir,
		}),
	})
	if err != nil {
		log.Error(err, "unable to build manager")
		os.Exit(1)
	}

	maxSurge := time.Duration(maxSurgeSecs) * time.Second

	// The webhook reads pods/deployments/replicasets/pvcs directly (uncached):
	// eviction QPS is tiny, and a direct reader avoids lazy-informer warmup
	// racing the first admission call.
	directClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to build direct client")
		os.Exit(1)
	}

	var stripLabelPrefixes []string
	for _, p := range strings.Split(stripPrefixes, ",") {
		if p = strings.TrimSpace(p); p != "" {
			stripLabelPrefixes = append(stripLabelPrefixes, p)
		}
	}

	mgr.GetWebhookServer().Register("/validate-eviction", &webhook.Admission{
		Handler: eviction.NewHandler(directClient, maxSurge, stripLabelPrefixes, ctrl.Log.WithName("webhook")),
	})

	if err := controller.NewSurgeReconciler(mgr.GetClient(), maxSurge, ctrl.Log.WithName("controller")).
		SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up surge controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "add healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		log.Error(err, "add readyz")
		os.Exit(1)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited")
		os.Exit(1)
	}
}
