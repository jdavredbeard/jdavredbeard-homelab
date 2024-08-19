package main

import (
	"context"
	"fmt"
	// "io"
	"encoding/json"
	"strings"
	"os"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

// const pulumiHomeEnv = "PULUMI_HOME"

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

// func runPulumiInputCmdSync(
// 	ctx context.Context,
// 	workspace auto.Workspace,
// 	stdin io.Reader,
// 	additionalOutputs []io.Writer,
// 	additionalErrorOutputs []io.Writer,
// 	args ...string,
// ) (string, string, int, error) {
// 	var env []string
// 	if workspace.PulumiHome() != "" {
// 		homeEnv := fmt.Sprintf("%s=%s", pulumiHomeEnv, workspace.PulumiHome())
// 		env = append(env, homeEnv)
// 	}
// 	if envvars := workspace.GetEnvVars(); envvars != nil {
// 		for k, v := range envvars {
// 			e := []string{k, v}
// 			env = append(env, strings.Join(e, "="))
// 		}
// 	}
// 	return workspace.PulumiCommand().Run(ctx,
// 		workspace.WorkDir(),
// 		stdin,
// 		additionalOutputs,
// 		additionalErrorOutputs,
// 		env,
// 		args...,
// 	)
// }

// func runPulumiCmdSync(
// 	ctx context.Context,
// 	workspace auto.Workspace,
// 	args ...string,
// ) (string, string, int, error) {
// 	return runPulumiInputCmdSync(ctx, workspace, nil, nil, nil, args...)
// }

// // listEnvironmentsForOrg returns the list of environments from the provided Organization
// func listEnvironmentsForOrg(ctx context.Context, orgName string, workspace auto.Workspace) ([]string, error) {
// 	args := []string{"env", "ls", "-o", orgName}
// 	stdout, stderr, errCode, err := runPulumiCmdSync(ctx, workspace, args...)
// 	if err != nil {
// 		return nil, newAutoError(fmt.Errorf("unable to list environments: %w", err), stdout, stderr, errCode)
// 	}
// 	envs := strings.Split(stdout, "\n")
// 	return envs, nil
// }

func runPulumiCmd(ctx context.Context, pulumiCmd auto.PulumiCommand, args []string) (string, string, int, error) {
	return pulumiCmd.Run(ctx, ".", nil,	nil, nil, nil, args...)
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
	PulumiConfig map[string]string `json:"pulumiConfig"`
}

func filterEnvsByConfigValue(ctx context.Context, org string, envs []string, configKey string, desiredConfigValue string, pulumiCommand auto.PulumiCommand) []string {
	wg := new(sync.WaitGroup)
	c := make(chan string, len(envs))

	for _, env := range envs {
		wg.Add(1)
		go func(env string) {
			defer wg.Done()
			configValue, err := getConfigValue(ctx, org, env, "pulumiConfig." + configKey, pulumiCommand)
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
	for env := range(c) {
		filteredEnvs = append(filteredEnvs, env)
	}
	return filteredEnvs
}

func main() {
	org := ""
	configKeyValue := ""
	configKey := ""
	desiredConfigValue := ""
	args := os.Args[1:]
	if len(args) == 2 {
		org = args[0]
		configKeyValue = args[1]
		configKey = strings.Split(configKeyValue, ":")[0]
		desiredConfigValue = strings.Split(configKeyValue, ":")[1]
	} else {
		fmt.Println("paralumi needs two arguments, organization and config key and value as {key}:{value}")
		os.Exit(1)
	}
	
	ctx := context.Background()

	localWorkspace, err := auto.NewLocalWorkspace(ctx)
	if err != nil {
		fmt.Printf("failed to create LocalWorkspace: %v", err)
		os.Exit(1)
	}

	envs, err := listEnvironmentsForOrg(ctx, org, localWorkspace.PulumiCommand())
	if err != nil {
		fmt.Printf("failed to listEnvironmentsForOrg: %v", err)
		os.Exit(1)
	}

	filteredEnvs := filterEnvsByConfigValue(ctx, org, envs, configKey, desiredConfigValue, localWorkspace.PulumiCommand())
	fmt.Println(filteredEnvs)
}