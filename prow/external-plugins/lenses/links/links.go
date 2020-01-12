// Package links provides a links viewer
package links

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"

	"k8s.io/test-infra/prow/spyglass/api"
)

var _ api.Lens = Lens{}

// Lens implements the build lens.
type Lens struct{}

// Header executes the "header" section of the template.
func (lens Lens) Header(artifacts []api.Artifact, resourceDir string, config json.RawMessage) string {
	return executeTemplate(resourceDir, "header", View{})
}

// View holds data to render the body template
type View struct {
	Data  []DataRow
	Links []LinkRow
}

type DataRow struct {
	Name string
	Data string
}

type LinkRow struct {
	Name  string
	Links []Link
}

type Link struct {
	Text string
	URL  string
}

type config struct {
	BuildLogMetadata []configElement `json:"buildLogMetadata"`
	BuildLogLinks    []configElement `json:"buildLogLinks"`
	ArtifactLinks    []configElement `json:"artifactLinks"`
}

type configElement struct {
	Name  string `json:"name"`
	Regex string `json:"regex"`
}

// Body returns the <body> content
func (lens Lens) Body(artifacts []api.Artifact, resourceDir string, data string, rawConfig json.RawMessage) string {

	var c config
	if err := json.Unmarshal(rawConfig, &c); err != nil {
		logrus.WithError(err).Error("Failed to decode lens config")
		return ""
	}

	buildLogsView := View{}

	// get build log
	var lines []byte
	var err error
	for _, a := range artifacts {
		if !strings.HasSuffix(a.JobPath(), "build-log.txt") {
			continue
		}

		lines, err = a.ReadAll()
		if err != nil {
			logrus.WithError(fmt.Errorf("error matching log")).Info("")
			continue
		}
	}

	if len(lines) > 0 {
		for _, buildLogMetadata := range c.BuildLogMetadata {
			matches, err := matchRegex(lines, buildLogMetadata.Regex)
			if err != nil {
				logrus.WithError(err).Info("Error matching build log.")
				continue
			}
			buildLogsView.Data = append(buildLogsView.Data, DataRow{Name: buildLogMetadata.Name, Data: strings.Join(matches, " ")})
		}
		for _, buildLogLink := range c.BuildLogLinks {
			links, err := matchLinkRegex(lines, buildLogLink.Regex)
			if err != nil {
				logrus.WithError(err).Info("Error matching build log.")
				continue
			}
			buildLogsView.Links = append(buildLogsView.Links, LinkRow{Name: buildLogLink.Name, Links: links})
		}
	}

	for _, artifactLink := range c.ArtifactLinks {
		var links []Link
		for _, a := range artifacts {
			if _, err := matchRegex([]byte(a.JobPath()), artifactLink.Regex); err != nil {
				logrus.WithError(err).Info("Error matching build log.")
				continue
			}
			links = append(links, Link{Text: path.Base(a.JobPath()), URL: a.CanonicalLink()})
		}
		if len(links) > 0 {
			buildLogsView.Links = append(buildLogsView.Links, LinkRow{Name: artifactLink.Name, Links: links})
		}
	}
	return executeTemplate(resourceDir, "body", buildLogsView)
}

func matchRegex(lines []byte, regexString string) ([]string, error) {
	regex, err := regexp.Compile(regexString)
	if err != nil {
		return nil, fmt.Errorf("failed to compile regex %q: %v", regex, err)
	}

	matches := regex.FindAllStringSubmatch(string(lines), -1)
	if len(matches) < 1 {
		return nil, fmt.Errorf("error matching log")
	}
	matchesArray := []string{}
	for i := 0; i < len(matches); i++ {
		if len(matches[0]) == 2 {
			matchesArray = append(matchesArray, matches[i][1])
		}
	}
	return matchesArray, nil
}

func matchLinkRegex(lines []byte, bld string) ([]Link, error) {
	regex, err := regexp.Compile(bld)
	if err != nil {
		return nil, fmt.Errorf("failed to compile regex %q: %v", bld, err)
	}

	matches := regex.FindAllStringSubmatch(string(lines), -1)
	if len(matches) < 1 {
		return nil, fmt.Errorf("error matching log")
	}
	links := []Link{}
	for i := 0; i < len(matches); i++ {
		if len(matches[0]) == 3 {
			links = append(links, Link{Text: matches[i][1], URL: strings.TrimSpace(matches[i][2])})
		}
	}
	return links, nil
}

// Callback is unused
func (lens Lens) Callback(artifacts []api.Artifact, resourceDir string, data string, rawConfig json.RawMessage) string {
	return ""
}

func executeTemplate(resourceDir, templateName string, data interface{}) string {
	t := template.New("template.html")
	_, err := t.ParseFiles(filepath.Join(resourceDir, "template.html"))
	if err != nil {
		return fmt.Sprintf("Failed to load template: %v", err)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, templateName, data); err != nil {
		logrus.WithError(err).Error("Error executing template.")
	}
	return buf.String()
}
