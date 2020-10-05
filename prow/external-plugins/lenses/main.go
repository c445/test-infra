/*
Copyright 2017 The Kubernetes Authors.

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

// Based on needs-rebase plugin

package main

import (
	"context"
	"flag"
	"io/ioutil"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	coreapi "k8s.io/api/core/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"k8s.io/test-infra/pkg/flagutil"
	prowapi "k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/deck/jobs"
	"k8s.io/test-infra/prow/external-plugins/lenses/links"
	prowflagutil "k8s.io/test-infra/prow/flagutil"
	"k8s.io/test-infra/prow/interrupts"
	"k8s.io/test-infra/prow/io"
	"k8s.io/test-infra/prow/spyglass"
	"k8s.io/test-infra/prow/spyglass/lenses"
	"k8s.io/test-infra/prow/spyglass/lenses/common"
)

type options struct {
	port int

	configPath    string
	jobConfigPath string
	pluginConfig  string

	spyglassFilesLocation string

	dryRun     bool
	kubernetes prowflagutil.KubernetesOptions
	storage    prowflagutil.StorageClientOptions

	updatePeriod time.Duration

	webhookSecretFile string
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.StringVar(&o.configPath, "config-path", "", "Path to config.yaml.")
	fs.StringVar(&o.jobConfigPath, "job-config-path", "", "Path to prow job configs.")
	fs.StringVar(&o.pluginConfig, "plugin-config", "/etc/plugins/plugins.yaml", "Path to plugin config file.")
	fs.StringVar(&o.spyglassFilesLocation, "spyglass-files-location", "/lenses", "Location of the static files for spyglass.")
	fs.BoolVar(&o.dryRun, "dry-run", true, "Dry run for testing. Uses API tokens but does not mutate.")
	fs.DurationVar(&o.updatePeriod, "update-period", time.Hour*24, "Period duration for periodic scans of all PRs.")
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")

	for _, group := range []flagutil.OptionGroup{&o.kubernetes, &o.storage} {
		group.AddFlags(fs)
	}
	_ = fs.Parse(os.Args[1:])
	return o
}

const spyglassLocalLensListenerAddr = "127.0.0.1:1235"

func main() {
	o := gatherOptions()

	logrus.SetFormatter(&logrus.JSONFormatter{})

	configAgent := &config.Agent{}
	if err := configAgent.Start(o.configPath, o.jobConfigPath); err != nil {
		logrus.WithError(err).Fatal("Error starting config agent.")
	}

	logLevel, err := logrus.ParseLevel(configAgent.Config().LogLevel)
	if err != nil {
		logrus.Fatalf("Could not parse loglevel %q: %v", configAgent.Config().LogLevel, err)
	}
	logrus.SetLevel(logLevel)

	buildClusterClients, err := o.kubernetes.BuildClusterClients(configAgent.Config().PodNamespace, false)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting Kubernetes client.")
	}
	podLogClients := make(map[string]jobs.PodLogClient)
	for clusterContext, client := range buildClusterClients {
		podLogClients[clusterContext] = &podLogClient{client: client}
	}

	restCfg, err := o.kubernetes.InfrastructureClusterConfig(false)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting infrastructure cluster config.")
	}
	mgr, err := manager.New(restCfg, manager.Options{
		Namespace:          configAgent.Config().ProwJobNamespace,
		MetricsBindAddress: "0",
		LeaderElection:     false,
	})
	if err != nil {
		logrus.WithError(err).Fatal("Error getting manager.")
	}
	// Force a cache for ProwJobs
	if _, err := mgr.GetCache().GetInformer(context.TODO(), &prowapi.ProwJob{}); err != nil {
		logrus.WithError(err).Fatal("Failed to get prowjob informer")
	}
	go func() {
		if err := mgr.Start(make(chan struct{})); err != nil {
			logrus.WithError(err).Fatal("Error starting manager.")
		} else {
			logrus.Info("Manager stopped gracefully.")
		}
	}()
	mgrSyncCtx, mgrSyncCtxCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer mgrSyncCtxCancel()
	if synced := mgr.GetCache().WaitForCacheSync(mgrSyncCtx.Done()); !synced {
		logrus.Fatal("Timed out waiting for cachesync")
	}

	ja := jobs.NewJobAgent(context.Background(), &pjListingClientWrapper{mgr.GetClient()}, false, false, podLogClients, configAgent.Config)
	ja.Start()

	ctx := context.TODO()
	opener, err := io.NewOpener(ctx, o.storage.GCSCredentialsFile, o.storage.S3CredentialsFile)
	if err != nil {
		logrus.WithError(err).Fatal("Error creating opener")
	}

	localLenses := []common.LensWithConfiguration{
		{
			Config: common.LensOpt{
				LensResourcesDir: lenses.ResourceDirForLens(o.spyglassFilesLocation, "links"),
				LensName:         "links",
				LensTitle:        "links",
			},
			Lens: links.Lens{},
		},
	}

	lensServer, err := common.NewLensServer(spyglassLocalLensListenerAddr, ja, spyglass.NewStorageArtifactFetcher(opener, configAgent.Config, false), spyglass.NewPodLogArtifactFetcher(ja), configAgent.Config, localLenses)
	if err != nil {
		logrus.Fatalf("Failed to start lens server: %v", err)
	}

	interrupts.ListenAndServe(lensServer, 5*time.Second)

	select {}
}

type podLogClient struct {
	client corev1.PodInterface
}

func (c *podLogClient) GetLogs(name, container string) ([]byte, error) {
	reader, err := c.client.GetLogs(name, &coreapi.PodLogOptions{Container: container}).Stream(context.TODO())
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ioutil.ReadAll(reader)
}

type pjListingClientWrapper struct {
	reader ctrlruntimeclient.Reader
}

func (w *pjListingClientWrapper) List(
	ctx context.Context,
	pjl *prowapi.ProwJobList,
	opts ...ctrlruntimeclient.ListOption) error {
	return w.reader.List(ctx, pjl, opts...)
}
