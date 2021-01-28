/*
Copyright 2018 The Kubernetes Authors.

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

package plugin

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/sets"

	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/pluginhelp"
	"k8s.io/test-infra/prow/repoowners"
)

const (
	// PluginName defines this plugin's registered name.
	PluginName = "mention"

	MentionNotificationName = "MENTIONNOTIFIER"
)

var (
	notificationRegex = regexp.MustCompile(`(?is)^\[` + MentionNotificationName + `\] *?([^\n]*)(?:\n\n(.*))?`)
)

func HelpProvider(_ []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	return &pluginhelp.PluginHelp{
			Description: "TODO: The owners-label plugin automatically adds labels to PRs based on the files they touch. Specifically, the 'labels' sections of OWNERS files are used to determine which labels apply to the changes. In our fork it also mentions the team if a team label is used. E.g. the label team/core-platform leads to a mention of @c445/core-platform",
		},
		nil
}

type ownersClient interface {
	FindLabelsForFile(path string) sets.String
}

type githubClient interface {
	GetPullRequestChanges(org, repo string, number int) ([]github.PullRequestChange, error)
	DeleteComment(org, repo string, ID int) error
	CreateComment(org, repo string, number int, comment string) error
	BotUserChecker() (func(candidate string) bool, error)
	ListIssueComments(org, repo string, number int) ([]github.IssueComment, error)
}

func HandlePullRequestEvent(log *logrus.Entry, githubClient github.Client, ownersClient repoowners.Interface, pre *github.PullRequestEvent) error {
	if pre.Action != github.PullRequestActionOpened && pre.Action != github.PullRequestActionReopened && pre.Action != github.PullRequestActionSynchronize {
		return nil
	}

	oc, err := ownersClient.LoadRepoOwners(pre.Repo.Owner.Login, pre.Repo.Name, pre.PullRequest.Base.Ref)
	if err != nil {
		return fmt.Errorf("error loading RepoOwners: %v", err)
	}

	return handle(githubClient, oc, log, pre)
}

func handle(ghc githubClient, oc ownersClient, log *logrus.Entry, pre *github.PullRequestEvent) error {
	org := pre.Repo.Owner.Login
	repo := pre.Repo.Name
	number := pre.Number

	// First see if there are any labels requested based on the files changed.
	changes, err := ghc.GetPullRequestChanges(org, repo, number)
	if err != nil {
		return fmt.Errorf("error getting PR changes: %v", err)
	}
	neededLabels := sets.NewString()
	for _, change := range changes {
		neededLabels.Insert(oc.FindLabelsForFile(change.Filename).List()...)
	}
	if neededLabels.Len() == 0 {
		// No labels requested for the given files. Return now to save API tokens.
		return nil
	}

	if err = updateMentionComment(ghc, log, pre, neededLabels); err != nil {
		return err
	}

	return nil
}

func updateMentionComment(ghc githubClient, log *logrus.Entry, pre *github.PullRequestEvent, neededLabels sets.String) error {

	fetchErr := func(context string, err error) error {
		return fmt.Errorf("failed to get %s for %s#%d: %v", context, pre.Repo.FullName, pre.PullRequest.Number, err)
	}

	botUserChecker, err := ghc.BotUserChecker()
	if err != nil {
		return fetchErr("bot name", err)
	}
	issueComments, err := ghc.ListIssueComments(pre.Repo.Owner.Login, pre.Repo.Name, pre.Number)
	if err != nil {
		return fetchErr("issue comments", err)
	}
	commentsFromIssueComments := commentsFromIssueComments(issueComments)

	notifications := filterComments(commentsFromIssueComments, notificationMatcher(botUserChecker))
	latestNotification := getLast(notifications)
	newMessage := updateNotification(neededLabels, pre.Repo.Owner.Login, latestNotification)
	if newMessage != nil {
		for _, notif := range notifications {
			if err := ghc.DeleteComment(pre.Repo.Owner.Login, pre.Repo.Name, notif.ID); err != nil {
				log.WithError(err).Errorf("Failed to delete comment from %s/%s#%d, ID: %d.", pre.Repo.Owner.Login, pre.Repo.Name, pre.Number, notif.ID)
			}
		}
		if err := ghc.CreateComment(pre.Repo.Owner.Login, pre.Repo.Name, pre.Number, *newMessage); err != nil {
			log.WithError(err).Errorf("Failed to create comment on %s/%s#%d: %q.", pre.Repo.Owner.Login, pre.Repo.Name, pre.Number, *newMessage)
		}
	}
	return nil
}

func updateNotification(neededLabels sets.String, org string, latestNotification *comment) *string {
	var teams []string
	for _, label := range neededLabels.List() {
		if strings.HasPrefix(label, "team/") {
			teams = append(teams, fmt.Sprintf("@%s/%s", org, strings.TrimPrefix(label, "team/")))

		}
	}
	message := fmt.Sprintf("[%s]: Please take a look: %s", MentionNotificationName, strings.Join(teams, " "))
	if latestNotification != nil && strings.Contains(latestNotification.Body, message) {
		return nil
	}
	return &message
}

func notificationMatcher(isBot func(string) bool) func(*comment) bool {
	return func(c *comment) bool {
		if !isBot(c.Author) {
			return false
		}
		match := notificationRegex.FindStringSubmatch(c.Body)
		return len(match) > 0
	}
}

type comment struct {
	Body        string
	Author      string
	CreatedAt   time.Time
	HTMLURL     string
	ID          int
	ReviewState github.ReviewState
}

func commentFromIssueComment(ic *github.IssueComment) *comment {
	if ic == nil {
		return nil
	}
	return &comment{
		Body:      ic.Body,
		Author:    ic.User.Login,
		CreatedAt: ic.CreatedAt,
		HTMLURL:   ic.HTMLURL,
		ID:        ic.ID,
	}
}

func commentsFromIssueComments(ics []github.IssueComment) []*comment {
	comments := make([]*comment, 0, len(ics))
	for i := range ics {
		comments = append(comments, commentFromIssueComment(&ics[i]))
	}
	return comments
}

func filterComments(comments []*comment, filter func(*comment) bool) []*comment {
	filtered := make([]*comment, 0, len(comments))
	for _, c := range comments {
		if filter(c) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func getLast(cs []*comment) *comment {
	if len(cs) == 0 {
		return nil
	}
	return cs[len(cs)-1]
}
