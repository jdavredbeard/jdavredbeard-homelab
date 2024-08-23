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

type Result struct {
	FQSN string
	Environment string
	Creates int
	Updates int
	Destroys int
}

type Command int

const (
	Up Command = iota
	Preview
)

func getAllResults(ctx context.Context, command Command, org string, project string, envs []string, stackName string, existingStacksInEnvs map[string][]string, localWorkspace auto.Workspace) []Result {
	wg := new(sync.WaitGroup)
	c := make(chan Result, len(envs))
	
	var dirName string

	switch command {
	case Up:
		dirName = "up-stdout"
	case Preview:
		dirName = "preview-stdout"
	default:
		fmt.Println("invalid Command enum passed to getAllResults - valid values are Up and Preview")
		os.Exit(1)
	}

	err := os.Mkdir(dirName, 0750)
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
			
			result, err := getResult(ctx, command, fqsn, env, localWorkspace)
			if err != nil {
				fmt.Printf("failed to getResult for %v for environment %v: %v\n", fqsn, env, err)
			} else {
				c <- result
			}
		}(env)
	}
	wg.Wait()
	close(c)
	var allResults []Result
	for result := range c {
		allResults = append(allResults, result)
	}
	return allResults
}

func getResult(ctx context.Context, command Command, fqsn string, env string, localWorkspace auto.Workspace) (Result, error) {
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

	var result Result

	switch command {
	case Up:
		stdoutOutputFile, err := os.Create(fmt.Sprintf("up-stdout/%v", env))
		if err != nil {
			fmt.Printf("failed to create file up-stdout/%v: %v", env, err)
		}
		defer stdoutOutputFile.Close()

		stdoutStreamer := optup.ProgressStreams(stdoutOutputFile)
		upResult, err := stack.Up(ctx, stdoutStreamer, optup.Diff())

		if err != nil {
			return Result{}, err
		}

		result = Result{
			FQSN: fqsn,
			Environment: env,
			Creates: (*upResult.Summary.ResourceChanges)["create"],
			Updates: (*upResult.Summary.ResourceChanges)["update"],
			Destroys: (*upResult.Summary.ResourceChanges)["destroy"],
		}
	case Preview:
		stdoutOutputFile, err := os.Create(fmt.Sprintf("preview-stdout/%v", env))
		if err != nil {
			fmt.Printf("failed to create file preview-stdout/%v: %v", env, err)
		}
		defer stdoutOutputFile.Close()

		stdoutStreamer := optpreview.ProgressStreams(stdoutOutputFile)
		previewResult, err := stack.Preview(ctx, stdoutStreamer, optpreview.Diff())

		if err != nil {
			return Result{}, err
		}
		result = Result{
			FQSN: fqsn,
			Environment: env,
			Creates: previewResult.ChangeSummary["create"],
			Updates: previewResult.ChangeSummary["update"],
			Destroys: previewResult.ChangeSummary["destroy"],
		}
	}
	
	return result, err
}

func printTable(ctx context.Context, results []Result) {
	fmt.Println()
	headerFmt := color.New(color.FgGreen, color.Underline).SprintfFunc()
	columnFmt := color.New(color.FgYellow).SprintfFunc()

	tbl := table.New("FQSN", "env", "creates", "updates", "destroys")
	tbl.WithHeaderFormatter(headerFmt).WithFirstColumnFormatter(columnFmt)

	for _, result := range results {
		tbl.AddRow(result.FQSN, result.Environment, result.Creates, result.Updates, result.Destroys)
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

	fmt.Printf("Getting all Environments for org: %v...\n", *org)
	envs, err := listEnvironmentsForOrg(ctx, *org, localWorkspace.PulumiCommand())
	if err != nil {
		fmt.Printf("failed to listEnvironmentsForOrg: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Filtering Environments by config %v...\n", *configKeyValue)
	filteredEnvs := filterEnvsByConfigValue(ctx, *org, envs, configKey, desiredConfigValue, localWorkspace.PulumiCommand())
	fmt.Printf("Found Environments: %v\n", filteredEnvs)

	project, err := localWorkspace.ProjectSettings(ctx)
	if err != nil {
		fmt.Printf("failed to get ProjectSettings: %v\n", err)
	}
	projectName := project.Name.String()

	fmt.Println()

	existingStacksInEnvs := getExistingStacksInEnvs(ctx, filteredEnvs, localWorkspace)
	
	switch command {
		case "preview": 
			results := getAllResults(ctx, Preview, *org, projectName, filteredEnvs, *stackName, existingStacksInEnvs, localWorkspace)
			printTable(ctx, results)
		case "up":
			results := getAllResults(ctx, Up, *org, projectName, filteredEnvs, *stackName, existingStacksInEnvs, localWorkspace)
			printTable(ctx, results)
	}	
}
