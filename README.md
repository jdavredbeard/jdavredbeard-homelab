# paralumi  

`paralumi` is an example custom CLI that uses the Automation API to deploy pulumi stacks to groups of environments in parallel.  
  
See [the Good/Bad/Ugly doc](https://docs.google.com/document/d/1a3fWffJ_xGyRfFDpVPclmwhHxYtaYtH-N9RBQP-jO5g/edit?usp=sharing) for the full writeup.  
  
## Setup  
1. Update the role arn in `environments/env.yaml` with your own AWS OIDC role. This yaml is used to generate multiple environments that will all point to the same AWS account for this example.  
1. Run `pulumi up` in `environments`.  
1. Compile the go code in `paralumi` with `cd paralumi` and `go build -o ./bin/paralumi ./src`
1. Run the compiled binary from the `example-stack` directory with `cd ../example-stack` and `../paralumi/bin/paralumi {preview/up} --org {organization} --config {key:value} --stackName {baseStackName}`

