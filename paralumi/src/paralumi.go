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

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
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

func openEnv(ctx context.Context, envName string, pulumiCmd auto.PulumiCommand) (environment, error) {
	args := []string{"env", "open", envName}
	stdout, stderr, errCode, err := runPulumiCmd(ctx, pulumiCmd, args)
	if err != nil {
		return environment{}, newAutoError(fmt.Errorf("unable to list environments: %w", err), stdout, stderr, errCode)
	}
	var env environment
	err = json.Unmarshal([]byte(stdout), &env)
	if err != nil {
		return environment{}, fmt.Errorf("unable to unmarshal environment value: %w", err)
	}
	return env, nil
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
		return "", fmt.Errorf("unable to unmarshal config value: %w", err)
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
				fmt.Printf("failed to getConfigValue %v for environment %v: %v", configKey, env, err)
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

func getAllPreviewResults(ctx context.Context, org string, project string, envs []string, stackName string, localWorkspace auto.Workspace) []PreviewResult {
	wg := new(sync.WaitGroup)
	c := make(chan PreviewResult, len(envs))

	for _, env := range envs {
		wg.Add(1)
		go func(env string) {
			defer wg.Done()
			envAndStack := env + "-" + stackName
			fqsn := auto.FullyQualifiedStackName(org, project, envAndStack)
			previewResult, err := getPreviewResult(ctx, fqsn, env, localWorkspace)
			if err != nil {
				fmt.Printf("failed to getPreviewResult for %v for environment %v: %v", fqsn, env)
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
		fmt.Printf("failed to UpsertStack: %v", err)
		os.Exit(1)
	}
	err = localWorkspace.AddEnvironments(ctx, fqsn, env)

	// stdoutStreamer := optpreview.ProgressStreams(os.Stdout)
	// previewResult, err := stack.Preview(ctx, stdoutStreamer, optpreview.Diff())

	previewResult, err := stack.Preview(ctx, optpreview.Diff())
	if err != nil {
		err = err.(autoError)
		return PreviewResult{}, newAutoError(fmt.Errorf("failed to Preview %v in %v", fqsn, env), previewResult.StdOut, previewResult.StdErr, 1)
	}
	return PreviewResult{
		FQSN: fqsn,
		ChangeSummary: previewResult.ChangeSummary,
		Environment: env,
	}, err
}

func main() {
	flagSet := flag.NewFlagSet("flagset", flag.ExitOnError)
	org := flagSet.String("org", "", "pulumi cloud organization")
	configKeyValue := flagSet.String("config", "", "config key and value to filter by: {key}:{value}")
	stackName := flagSet.String("stackName", "", "stack name")

	command := ""
	commands := []string{"preview", "up"}
	command = os.Args[1]
	if !slices.Contains(commands, command) {
		fmt.Printf("valid paralumi commands include %v", commands)
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
		fmt.Printf("paralumi requires flags: -stackName, -config, and -org")
		os.Exit(1)
	}

	ctx := context.Background()

	localWorkspace, err := auto.NewLocalWorkspace(ctx, auto.WorkDir("."))
	if err != nil {
		fmt.Printf("failed to create LocalWorkspace: %v", err)
		os.Exit(1)
	}

	envs, err := listEnvironmentsForOrg(ctx, *org, localWorkspace.PulumiCommand())
	if err != nil {
		fmt.Printf("failed to listEnvironmentsForOrg: %v", err)
		os.Exit(1)
	}

	filteredEnvs := filterEnvsByConfigValue(ctx, *org, envs, configKey, desiredConfigValue, localWorkspace.PulumiCommand())
	// fmt.Println(filteredEnvs)

	// check if environments already associated with workspace
	// existingWorkspaceEnvs, err := localWorkspace.ListEnvironments(ctx, *stackName)
	// fmt.Printf("Existing local workspace environments: %v\n", existingWorkspaceEnvs)

	// TODO: remove existing environments ()
	
	project, err := localWorkspace.ProjectSettings(ctx)
	if err != nil {
		fmt.Printf("failed to get ProjectSettings: %v\n", err)
	}
	projectName := project.Name.String()

	fmt.Println()
	
	previewResults := getAllPreviewResults(ctx, *org, projectName, filteredEnvs, *stackName, localWorkspace)

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
