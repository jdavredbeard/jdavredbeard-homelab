import { LocalProgramArgs, LocalWorkspace } from "@pulumi/pulumi/automation";
import * as upath from "upath";
import * as process from "process";

const args = process.argv.slice(2);
let destroy = false;
if (args.length > 0 && args[0]) {
    destroy = args[0] === "destroy";
}

const run = async () => {
    

    // Create our stack using a local program
    // in the ../stack directory
    const args: LocalProgramArgs = {
        stackName: "dev",
        workDir: upath.joinSafe(__dirname, "..", "stack"),
    };

    // create (or select if one already exists) a stack that uses our local program
    const stack = await LocalWorkspace.createOrSelectStack(args);
    console.info("successfully initialized stack");

    console.info("refreshing stack...");
    await stack.refresh({ onOutput: console.info });
    console.info("refresh complete");

    if (destroy) {
        console.info("destroying stack...");
        await stack.destroy({onOutput: console.info});
        console.info("stack destroy complete");
        process.exit(0);
    }

    console.info("updating stack...");
    const upRes = await stack.up({ onOutput: console.info });
    console.log(`update summary: \n${JSON.stringify(upRes.summary, null, 4)}`);
};

run();

