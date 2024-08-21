import * as pulumi from "@pulumi/pulumi";
import * as pulumiservice from "@pulumi/pulumiservice";
import * as fs from 'fs';
import * as yaml from 'yaml';

let animals = ["wombat", "platypus", "kangaroo", "crocodile", "koala"]

let envContents = fs.readFileSync('env.yaml', 'utf8');
let envContentsYaml = yaml.parse(envContents);

// create dev group envs
envContentsYaml.values.pulumiConfig.group = "dev";

for (let i=0; i<5; i++) {
    envContentsYaml.values.pulumiConfig.animal = animals[i]
    new pulumiservice.Environment(`environment-dev-${i}`, {
        name: `paralumi-dev-${i}`,
        organization: "jdavenport-pulumi-corp",
        yaml: new pulumi.asset.StringAsset(yaml.stringify(envContentsYaml)),
    });

}

// create staging group envs
envContentsYaml = yaml.parse(envContents);
envContentsYaml.values.pulumiConfig.group = "staging";
for (let i=0; i<4; i++) {
    envContentsYaml.values.pulumiConfig.animal = animals[i]
    new pulumiservice.Environment(`environment-staging-${i}`, {
        name: `paralumi-staging-${i}`,
        organization: "jdavenport-pulumi-corp",
        yaml: new pulumi.asset.StringAsset(yaml.stringify(envContentsYaml)),
    });
}

// create prod group envs
envContentsYaml = yaml.parse(envContents);
envContentsYaml.values.pulumiConfig.group = "prod";
for (let i=0; i<3; i++) {
    envContentsYaml.values.pulumiConfig.animal = animals[i]
    new pulumiservice.Environment(`environment-prod-${i}`, {
        name: `paralumi-prod-${i}`,
        organization: "jdavenport-pulumi-corp",
        yaml: new pulumi.asset.StringAsset(yaml.stringify(envContentsYaml)),
    });
}