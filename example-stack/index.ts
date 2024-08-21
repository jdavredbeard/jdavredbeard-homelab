import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

let config = new pulumi.Config()
let animal = config.get('animal')
let group = config.get('group')

console.log(animal, group)

const foo = new aws.ssm.Parameter("animal-group", {
    type: aws.ssm.ParameterType.String,
    value: `${animal}-${group}`,
});

export const animal_group = foo.value