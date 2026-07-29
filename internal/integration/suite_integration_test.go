//go:build integration

// Package integration runs the detach flow against a real apiserver (envtest).
// envtest does NOT run kube-controller-manager, so the ReplicaSet's own reaction
// to detaching a pod is not exercised here (that's the on-cluster e2e's job);
// what this covers is the operator's real API interactions — the merge patch
// that removes pod-template-hash, label-selector List for readiness, and the
// controller's delete — against genuine apiserver semantics.
package integration

import (
	"os"
	"testing"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{}
	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest (set KUBEBUILDER_ASSETS via `setup-envtest use`): " + err.Error())
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		_ = testEnv.Stop()
		panic(err)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}
