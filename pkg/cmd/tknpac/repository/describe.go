package repository

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"text/template"

	"github.com/AlecAivazis/survey/v2"
	"github.com/jonboulle/clockwork"
	"github.com/juju/ansiterm"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/apis/pipelinesascode/v1alpha1"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/cli"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/cli/prompt"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/cmd/tknpac/completion"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/formatting"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/params"
	"github.com/openshift-pipelines/pipelines-as-code/pkg/sort"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	describeTemplate = `{{ $.ColorScheme.Bold "Name" }}:	{{.Repository.Name}}
{{ $.ColorScheme.Bold "Namespace" }}:	{{.Repository.Namespace}}
{{ $.ColorScheme.Bold "URL" }}:	{{.Repository.Spec.URL}}

{{- if eq (len .Repository.Status) 0 }}

{{ $.ColorScheme.Dimmed "No runs has started."}}
{{- else }}

{{- $status := (index .Statuses 0) }}

{{ $.ColorScheme.Underline "Last Run:" }} {{ $.ColorScheme.ColorStatus (index $status.Status.Conditions 0).Reason  }}

{{ $.ColorScheme.Bold "PipelineRun" }}:	{{ $.ColorScheme.HyperLink $status.PipelineRunName $status.LogURL }}
{{ $.ColorScheme.Bold "Event" }}:	{{ $status.EventType }}
{{ $.ColorScheme.Bold "Branch" }}:	{{ sanitizeBranch $status.TargetBranch }}
{{ $.ColorScheme.Bold "Commit URL" }}:	{{ $status.SHAURL }}
{{ $.ColorScheme.Bold "Commit Title" }}:	{{ $status.Title }}
{{ $.ColorScheme.Bold "StartTime" }}:	{{ formatTime $status.StartTime $.Clock }}
{{ $.ColorScheme.Bold "Duration" }}:	{{ formatDuration $status.StartTime $status.CompletionTime }}

{{- if gt (len .Repository.Status) 1 }}

{{ $.ColorScheme.Underline "Other Runs:" }}

STATUS	Event	Branch	 SHA	 STARTED TIME	DURATION	PIPELINERUN
――――――	―――――	――――――	 ―――	 ――――――――――――	――――――――	―――――――――――
{{- range $i, $st := (slice .Statuses 1 (len .Repository.Status)) }}
{{ formatStatus $st $.ColorScheme $.Clock }}
{{- end }}
{{- end }}
{{- end }}

`
)

func formatStatus(status v1alpha1.RepositoryRunStatus, cs *cli.ColorScheme, c clockwork.Clock) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
		cs.ColorStatus(status.Status.Conditions[0].Reason),
		*status.EventType,
		formatting.SanitizeBranch(*status.TargetBranch),
		cs.HyperLink(formatting.ShortSHA(*status.SHA), *status.SHAURL),
		formatting.Age(status.StartTime, c),
		formatting.Duration(status.StartTime, status.CompletionTime),
		cs.HyperLink(status.PipelineRunName, *status.LogURL))
}

func askRepo(ctx context.Context, cs *params.Run, namespace string) (*v1alpha1.Repository, error) {
	repositories, err := cs.Clients.PipelineAsCode.PipelinesascodeV1alpha1().Repositories(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	if len(repositories.Items) == 0 {
		return nil, fmt.Errorf("no repo found")
	}
	if len(repositories.Items) == 1 {
		return &repositories.Items[0], nil
	}

	allRepositories := []string{}
	for _, repository := range repositories.Items {
		repoOwner, err := formatting.GetRepoOwnerFromGHURL(repository.Spec.URL)
		if err != nil {
			return nil, err
		}
		allRepositories = append(allRepositories,
			fmt.Sprintf("%s - %s",
				repository.GetName(),
				repoOwner))
	}

	var replyString string
	if err := prompt.SurveyAskOne(&survey.Select{
		Message: "Select a repository",
		Options: allRepositories,
	}, &replyString); err != nil {
		return nil, err
	}

	if replyString == "" {
		return nil, fmt.Errorf("you need to choose a repository")
	}
	replyName := strings.Fields(replyString)[0]

	for _, repository := range repositories.Items {
		if repository.GetName() == replyName {
			return &repository, nil
		}
	}

	return nil, fmt.Errorf("cannot match repository")
}

func DescribeCommand(run *params.Run, ioStreams *cli.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "describe",
		Aliases: []string{"desc"},
		Short:   "Describe a repository",
		Annotations: map[string]string{
			"commandType": "main",
		},
		ValidArgsFunction: completion.ParentCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			var repoName string
			opts := cli.NewCliOptions(cmd)

			opts.Namespace, err = cmd.Flags().GetString(namespaceFlag)
			if err != nil {
				return err
			}

			if len(args) > 0 {
				repoName = args[0]
			}

			ctx := context.Background()
			clock := clockwork.NewRealClock()
			err = run.Clients.NewClients(ctx, &run.Info)
			if err != nil {
				return err
			}
			return describe(ctx, run, clock, opts, ioStreams, repoName)
		},
	}

	cmd.Flags().StringP(
		namespaceFlag, "n", "", "If present, the namespace scope for this CLI request")

	_ = cmd.RegisterFlagCompletionFunc(namespaceFlag,
		func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return completion.BaseCompletion(namespaceFlag, args)
		},
	)
	return cmd
}

func describe(ctx context.Context, cs *params.Run, clock clockwork.Clock, opts *cli.PacCliOpts,
	ioStreams *cli.IOStreams, repoName string) error {
	var repository *v1alpha1.Repository
	var err error

	if opts.Namespace != "" {
		cs.Info.Kube.Namespace = opts.Namespace
	}

	if repoName != "" {
		repository, err = cs.Clients.PipelineAsCode.PipelinesascodeV1alpha1().Repositories(cs.Info.Kube.Namespace).Get(ctx,
			repoName, metav1.GetOptions{})
		if err != nil {
			return err
		}
	} else {
		repository, err = askRepo(ctx, cs, cs.Info.Kube.Namespace)
		if err != nil {
			return err
		}
	}

	colorScheme := ioStreams.ColorScheme()

	funcMap := template.FuncMap{
		"formatStatus":    formatStatus,
		"formatEventType": formatting.CamelCasit,
		"formatDuration":  formatting.Duration,
		"formatTime":      formatting.Age,
		"sanitizeBranch":  formatting.SanitizeBranch,
		"shortSHA":        formatting.ShortSHA,
	}

	data := struct {
		Repository  *v1alpha1.Repository
		Statuses    []v1alpha1.RepositoryRunStatus
		ColorScheme *cli.ColorScheme
		Clock       clockwork.Clock
	}{
		Repository:  repository,
		Statuses:    sort.RepositorySortRunStatus(repository.Status),
		ColorScheme: colorScheme,
		Clock:       clock,
	}

	w := ansiterm.NewTabWriter(ioStreams.Out, 0, 5, 3, ' ', tabwriter.TabIndent)
	t := template.Must(template.New("Describe Repository").Funcs(funcMap).Parse(describeTemplate))

	if err := t.Execute(w, data); err != nil {
		return err
	}

	return w.Flush()
}