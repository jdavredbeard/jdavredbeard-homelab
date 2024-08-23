package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"log"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/fatih/color"
  	"github.com/rodaine/table"
)

type autoError struct {
	stdout string
	stderr string
	code   int
	err    error
}

func newAutoError(err error, stdout, stderr string, code int) autoError {
	return autoError{
		stdout,
		stderr,
		code,
		err,
	}
}

func (ae autoError) Error() string {
	return fmt.Sprintf("%s\ncode: %d\nstdout: %s\nstderr: %s\n", ae.err, ae.code, ae.stdout, ae.stderr)
}

func runPulumiCmd(ctx context.Context, pulumiCmd auto.PulumiCommand, args []string) (string, string, int, error) {
	return pulumiCmd.Run(ctx, ".", nil, nil, nil, nil, args...)
}

func listEnvironmentsForOrg(ctx context.Context, orgName string, pulumiCmd auto.PulumiCommand) ([]string, error) {
	args := []string{"env", "ls", "-o", orgName}
	stdout, stderr, errCode, err := runPulumiCmd(ctx, pulumiCmd, args)
	if err != nil {
		return nil, newAutoError(fmt.Errorf("unable to list environments: %w", err), stdout, stderr, errCode)
	}
	envs := strings.Split(strings.TrimSpace(stdout), "\n")
	return envs, nil
}

func getConfigValue(ctx context.Context, orgName string, envName string, configPath string, pulumiCmd auto.PulumiCommand) (string, error) {
	args := []string{"env", "get", orgName + "/" + envName, configPath, "--value", "json"}
	stdout, stderr, errCode, err := runPulumiCmd(ctx, pulumiCmd, args)
	if err != nil {
		return "", newAutoError(fmt.Errorf("unable to get config value: %w", err), stdout, stderr, errCode)
	}
	var value string
	err = json.Unmarshal([]byte(stdout), &value)
	if err != nil {
		return "", fmt.Errorf("unable to unmarshal config value: %w\n", err)
	}
	return value, nil
}

type environment struct {
	EnvironmentVariables map[string]string `json:"environmentVariables"`
	PulumiConfig         map[string]string `json:"pulumiConfig"`
}

func filterEnvsByConfigValue(ctx context.Context, org string, envs []string, configKey string, desiredConfigValue string, pulumiCommand auto.PulumiCommand) []string {
	wg := new(sync.WaitGroup)
	c := make(chan string, len(envs))

	for _, env := range envs {
		wg.Add(1)
		go func(env string) {
			defer wg.Done()
			configValue, err := getConfigValue(ctx, org, env, "pulumiConfig."+configKey, pulumiCommand)
			if err != nil {
				fmt.Printf("failed to getConfigValue %v for environment %v: %v\n", configKey, env, err)
			} else {
				if configValue == desiredConfigValue {
					c <- env
				}
			}
		}(env)
	}
	wg.Wait()
	close(c)
	var filteredEnvs []string
	for env := range c {
		filteredEnvs = append(filteredEnvs, env)
	}
	return filteredEnvs
}

type PreviewResult struct {
	FQSN string
	ChangeSummary map[apitype.OpType]int
	Environment string
}

func getAllPreviewResults(ctx context.Context, org string, project string, envs []string, stackName string, existingStacksInEnvs map[string][]string, localWorkspace auto.Workspace) []PreviewResult {
	wg := new(sync.WaitGroup)
	c := make(chan PreviewResult, len(envs))

	err := os.Mkdir("preview-stdout", 0750)
	if err != nil && !os.IsExist(err) {
		log.Fatal(err)
	}

	for _, env := range envs {
		wg.Add(1)
		go func(env string) {
			defer wg.Done()
			var fqsn string
			if len(existingStacksInEnvs[env]) == 0 {
				envAndStack := env + "-" + stackName
				fqsn = auto.FullyQualifiedStackName(org, project, envAndStack)
			} else {
				fqsn = auto.FullyQualifiedStackName(org, project, existingStacksInEnvs[env][0])
			}
			
			previewResult, err := getPreviewResult(ctx, fqsn, env, localWorkspace)
			if err != nil {
				fmt.Printf("failed to getPreviewResult for %v for environment %v: %v\n", fqsn, env, err)
			} else {
				c <- previewResult
			}
		}(env)
	}
	wg.Wait()
	close(c)
	var allPreviewResults []PreviewResult
	for result := range c {
		allPreviewResults = append(allPreviewResults, result)
	}
	return allPreviewResults
}

func getPreviewResult(ctx context.Context, fqsn string, env string, localWorkspace auto.Workspace) (PreviewResult, error) {
	fmt.Printf("Running Preview on stack %v in env %v...\n", fqsn, env)
	stack, err := auto.UpsertStack(ctx, fqsn, localWorkspace)
	if err != nil {
		fmt.Printf("failed to UpsertStack: %v\n", err)
	}
	// check if environments already associated with workspace
	existingWorkspaceEnvs, err := localWorkspace.ListEnvironments(ctx, fqsn)
	if !slices.Contains(existingWorkspaceEnvs, env) {
		err = localWorkspace.AddEnvironments(ctx, fqsn, env)
		if err != nil {
			fmt.Printf("failed to AddEnvironments %v for %v\n", env, fqsn)
		}
	}

	stdoutOutputFile, err := os.Create(fmt.Sprintf("preview-stdout/%v", env))
	if err != nil {
		fmt.Printf("failed to create file preview-stdout/%v: %v", env, err)
	}
	defer stdoutOutputFile.Close()

	stdoutStreamer := optpreview.ProgressStreams(stdoutOutputFile)
	previewResult, err := stack.Preview(ctx, stdoutStreamer, optpreview.Diff())

	if err != nil {
		return PreviewResult{}, err
	}
	return PreviewResult{
		FQSN: fqsn,
		ChangeSummary: previewResult.ChangeSummary,
		Environment: env,
	}, err
}

func printPreviewTable(ctx context.Context, org string, project string, envs []string, stack string, existingStacksInEnvs map[string][]string, localWorkspace auto.Workspace) {
	previewResults := getAllPreviewResults(ctx, org, project, envs, stack, existingStacksInEnvs, localWorkspace)

	// draw the table
	fmt.Println()
	headerFmt := color.New(color.FgGreen, color.Underline).SprintfFunc()
	columnFmt := color.New(color.FgYellow).SprintfFunc()

	tbl := table.New("FQSN", "env", "creates", "updates", "destroys")
	tbl.WithHeaderFormatter(headerFmt).WithFirstColumnFormatter(columnFmt)

	for _, result := range previewResults {
		tbl.AddRow(result.FQSN, result.Environment, result.ChangeSummary["create"], result.ChangeSummary["update"], result.ChangeSummary["destroy"])
	}

	tbl.Print()
}

type UpResult struct {
	FQSN string
	UpdateSummary auto.UpdateSummary
	Environment string
}

func getAllUpResults(ctx context.Context, org string, project string, envs []string, stackName string, existingStacksInEnvs map[string][]string, localWorkspace auto.Workspace) []UpResult {
	wg := new(sync.WaitGroup)
	c := make(chan UpResult, len(envs))

	err := os.Mkdir("up-stdout", 0750)
	if err != nil && !os.IsExist(err) {
		log.Fatal(err)
	}

	for _, env := range envs {
		wg.Add(1)
		go func(env string) {
			defer wg.Done()
			var fqsn string
			if len(existingStacksInEnvs[env]) == 0 {
				envAndStack := env + "-" + stackName
				fqsn = auto.FullyQualifiedStackName(org, project, envAndStack)
			} else {
				fqsn = auto.FullyQualifiedStackName(org, project, existingStacksInEnvs[env][0])
			}
			
			upResult, err := getUpResult(ctx, fqsn, env, localWorkspace)
			if err != nil {
				fmt.Printf("failed to getUpResult for %v for environment %v: %v\n", fqsn, env, err)
			} else {
				c <- upResult
			}
		}(env)
	}
	wg.Wait()
	close(c)
	var allUpResults []UpResult
	for result := range c {
		allUpResults = append(allUpResults, result)
	}
	return allUpResults
}

func getUpResult(ctx context.Context, fqsn string, env string, localWorkspace auto.Workspace) (UpResult, error) {
	fmt.Printf("Running Up on stack %v in env %v...\n", fqsn, env)
	stack, err := auto.UpsertStack(ctx, fqsn, localWorkspace)
	if err != nil {
		fmt.Printf("failed to UpsertStack: %v\n", err)
	}
	// check if environments already associated with workspace
	existingWorkspaceEnvs, err := localWorkspace.ListEnvironments(ctx, fqsn)
	if !slices.Contains(existingWorkspaceEnvs, env) {
		err = localWorkspace.AddEnvironments(ctx, fqsn, env)
		if err != nil {
			fmt.Printf("failed to AddEnvironments %v for %v: %v\n", env, fqsn, err)
		}
	}

	stdoutOutputFile, err := os.Create(fmt.Sprintf("up-stdout/%v", env))
	if err != nil {
		fmt.Printf("failed to create file up-stdout/%v: %v", env, err)
	}
	defer stdoutOutputFile.Close()

	stdoutStreamer := optup.ProgressStreams(stdoutOutputFile)
	upResult, err := stack.Up(ctx, stdoutStreamer, optup.Diff())

	if err != nil {
		return UpResult{}, err
	}
	return UpResult{
		FQSN: fqsn,
		UpdateSummary: upResult.Summary,
		Environment: env,
	}, err
}

func printUpTable(ctx context.Context, org string, project string, envs []string, stack string, existingStacksInEnvs map[string][]string, localWorkspace auto.Workspace) {
	upResults := getAllUpResults(ctx, org, project, envs, stack, existingStacksInEnvs, localWorkspace)

	// draw the table
	fmt.Println()
	headerFmt := color.New(color.FgGreen, color.Underline).SprintfFunc()
	columnFmt := color.New(color.FgYellow).SprintfFunc()

	tbl := table.New("FQSN", "env", "creates", "updates", "destroys")
	tbl.WithHeaderFormatter(headerFmt).WithFirstColumnFormatter(columnFmt)

	for _, result := range upResults {
		changes := *result.UpdateSummary.ResourceChanges
		tbl.AddRow(result.FQSN, result.Environment, changes["create"], changes["update"], changes["destroy"])
	}

	tbl.Print()
}

func getExistingStacksInEnvs(ctx context.Context, envs []string, localWorkspace auto.Workspace) map[string][]string {
	workspaceStackSummaries, err := localWorkspace.ListStacks(ctx)
	if err != nil {
		fmt.Printf("failed to ListStacks in localWorkspace: %v\n", err)
		os.Exit(1)
	}

	stacksInEnv := map[string][]string{}

	for _, summary := range workspaceStackSummaries {
		existingStackEnvs, err := localWorkspace.ListEnvironments(ctx, summary.Name)
		if err != nil {
			fmt.Printf("failed to ListEnvironments in localWorkspace: %v\n", err)
			os.Exit(1)
		}
		for _, existingStackEnv := range existingStackEnvs {
			if slices.Contains(envs, existingStackEnv) {
				stacksInEnv[existingStackEnv] = append(stacksInEnv[existingStackEnv], summary.Name)
			}
		}
	}

	for env, stacks := range stacksInEnv {
		if len(stacks) > 1 {
			fmt.Printf("multiple existing stacks %v in environment %v: paralumi requires one stack per environment.", stacks, env)
			os.Exit(1)
		}
	}

	return stacksInEnv
}

func main() {
	flagSet := flag.NewFlagSet("flagset", flag.ExitOnError)
	org := flagSet.String("org", "", "pulumi cloud organization")
	configKeyValue := flagSet.String("config", "", "config key and value to filter by: {key}:{value}")
	stackName := flagSet.String("stackName", "", "stack name")

	command := ""
	commands := []string{"preview", "up"}
	if len(os.Args) == 1 {
		fmt.Println("paralumi usage: paralumi {preview/up} --org {organization} --config {key}:{value} --stackName {stackName}")
		os.Exit(1)
	}
	command = os.Args[1]
	if !slices.Contains(commands, command) {
		fmt.Printf("valid paralumi commands include %v\n", commands)
		os.Exit(1)
	}

	configKey := ""
	desiredConfigValue := ""
	flagSet.Parse(os.Args[2:])
	if *org != "" && *configKeyValue != "" && *stackName != "" {
		configKey = strings.Split(*configKeyValue, ":")[0]
		desiredConfigValue = strings.Split(*configKeyValue, ":")[1]
	} else {
		fmt.Println(*org, *configKeyValue, *stackName)
		fmt.Printf("paralumi requires flags: -stackName, -config, and -org\n")
		os.Exit(1)
	}

	ctx := context.Background()

	localWorkspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir("."))
	if err != nil {
		fmt.Printf("failed to create LocalWorkspace: %v\n", err)
		os.Exit(1)
	}

	envs, err := listEnvironmentsForOrg(ctx, *org, localWorkspace.PulumiCommand())
	if err != nil {
		fmt.Printf("failed to listEnvironmentsForOrg: %v\n", err)
		os.Exit(1)
	}

	filteredEnvs := filterEnvsByConfigValue(ctx, *org, envs, configKey, desiredConfigValue, localWorkspace.PulumiCommand())
	
	project, err := localWorkspace.ProjectSettings(ctx)
	if err != nil {
		fmt.Printf("failed to get ProjectSettings: %v\n", err)
	}
	projectName := project.Name.String()

	fmt.Println()

	existingStacksInEnvs := getExistingStacksInEnvs(ctx, filteredEnvs, localWorkspace)
	
	switch command {
		case "preview": 
			printPreviewTable(ctx, *org, projectName, filteredEnvs, *stackName, existingStacksInEnvs, localWorkspace)
		case "up":
			printUpTable(ctx, *org, projectName, filteredEnvs, *stackName, existingStacksInEnvs, localWorkspace)
	}
	
}
