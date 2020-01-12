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
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"k8s.io/test-infra/pkg/flagutil"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/config/secret"
	"k8s.io/test-infra/prow/external-plugins/mention/plugin"
	prowflagutil "k8s.io/test-infra/prow/flagutil"
	"k8s.io/test-infra/prow/git/v2"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/interrupts"
	"k8s.io/test-infra/prow/pluginhelp/externalplugins"
	"k8s.io/test-infra/prow/plugins"
	"k8s.io/test-infra/prow/plugins/ownersconfig"
	"k8s.io/test-infra/prow/repoowners"
)

type options struct {
	port int

	configPath    string
	jobConfigPath string
	pluginConfig  string

	dryRun bool
	github prowflagutil.GitHubOptions

	updatePeriod time.Duration

	webhookSecretFile string
}

func (o *options) Validate() error {
	for idx, group := range []flagutil.OptionGroup{&o.github} {
		if err := group.Validate(o.dryRun); err != nil {
			return fmt.Errorf("%d: %w", idx, err)
		}
	}

	return nil
}

func gatherOptions() options {
	o := options{}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.StringVar(&o.configPath, "config-path", "", "Path to config.yaml.")
	fs.StringVar(&o.jobConfigPath, "job-config-path", "", "Path to prow job configs.")
	fs.StringVar(&o.pluginConfig, "plugin-config", "/etc/plugins/plugins.yaml", "Path to plugin config file.")
	fs.BoolVar(&o.dryRun, "dry-run", true, "Dry run for testing. Uses API tokens but does not mutate.")
	fs.DurationVar(&o.updatePeriod, "update-period", time.Hour*24, "Period duration for periodic scans of all PRs.")
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/webhook/hmac", "Path to the file containing the GitHub HMAC secret.")

	for _, group := range []flagutil.OptionGroup{&o.github} {
		group.AddFlags(fs)
	}
	_ = fs.Parse(os.Args[1:])
	return o
}

func main() {
	o := gatherOptions()
	if err := o.Validate(); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

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
	log := logrus.StandardLogger().WithField("plugin", plugin.PluginName)

	secretAgent := &secret.Agent{}
	if err := secretAgent.Start([]string{o.github.TokenPath, o.webhookSecretFile}); err != nil {
		logrus.WithError(err).Fatal("Error starting secrets agent.")
	}

	pluginAgent := &plugins.ConfigAgent{}
	if err := pluginAgent.Start(o.pluginConfig, false); err != nil {
		log.WithError(err).Fatalf("Error loading plugin config from %q.", o.pluginConfig)
	}

	githubClient, err := o.github.GitHubClient(secretAgent, o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting GitHub client.")
	}
	githubClient.Throttle(360, 360)

	gitClient, err := o.github.GitClient(secretAgent, o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting Git client.")
	}

	mdYAMLEnabled := func(org, repo string) bool {
		return pluginAgent.Config().MDYAMLEnabled(org, repo)
	}
	skipCollaborators := func(org, repo string) bool {
		return pluginAgent.Config().SkipCollaborators(org, repo)
	}
	ownersDirBlacklist := func() config.OwnersDirBlacklist {
		return configAgent.Config().OwnersDirBlacklist
	}
	resolver := func(org, repo string) ownersconfig.Filenames {
		return pluginAgent.Config().OwnersFilenames(org, repo)
	}
	ownersClient := repoowners.NewClient(git.ClientFactoryFrom(gitClient), githubClient, mdYAMLEnabled, skipCollaborators, ownersDirBlacklist, resolver)

	server := &Server{
		GitHubClient:   githubClient,
		OwnersClient:   ownersClient,
		tokenGenerator: secretAgent.GetTokenGenerator(o.webhookSecretFile),
		ghc:            githubClient,
		log:            log,
	}

	defer interrupts.WaitForGracefulShutdown()

	mux := http.NewServeMux()
	mux.Handle("/", server)
	externalplugins.ServeExternalPluginHelp(mux, log, plugin.HelpProvider)
	httpServer := &http.Server{Addr: ":" + strconv.Itoa(o.port), Handler: mux}
	interrupts.ListenAndServe(httpServer, 5*time.Second)
}

// Server implements http.Handler. It validates incoming GitHub webhooks and
// then dispatches them to the appropriate plugins.
type Server struct {
	GitHubClient   github.Client
	OwnersClient   repoowners.Interface
	tokenGenerator func() []byte
	ghc            github.Client
	log            *logrus.Entry
}

// ServeHTTP validates an incoming webhook and puts it into the event channel.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO: Move webhook handling logic out of hook binary so that we don't have to import all
	// plugins just to validate the webhook.
	eventType, eventGUID, payload, ok, _ := github.ValidateWebhook(w, r, s.tokenGenerator)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(w, "Event received. Have a nice day.")

	if err := s.handleEvent(eventType, eventGUID, payload); err != nil {
		logrus.WithError(err).Error("Error parsing event.")
	}
}

func (s *Server) handleEvent(eventType, eventGUID string, payload []byte) error {
	l := s.log.WithFields(
		logrus.Fields{
			"event-type":     eventType,
			github.EventGUID: eventGUID,
		},
	)
	switch eventType {
	case "pull_request":
		var pre github.PullRequestEvent
		if err := json.Unmarshal(payload, &pre); err != nil {
			return err
		}
		go func() {
			if err := plugin.HandlePullRequestEvent(l, s.GitHubClient, s.OwnersClient, &pre); err != nil {
				l.WithField("event-type", eventType).WithError(err).Info("Error handling event.")
			}
		}()
	default:
		s.log.Debugf("received an event of type %q but didn't ask for it", eventType)
	}
	return nil
}
