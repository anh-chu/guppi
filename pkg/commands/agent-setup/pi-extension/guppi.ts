export default function (pi) {
  const guppiBin = process.env.GUPPI_BIN || "__GUPPI_BIN__";
  const { spawnSync } = require("child_process");

  const notify = (status, message) => {
    try {
      spawnSync(guppiBin, ["notify", "-t", "pi", "-s", status, "-m", message], { stdio: "ignore" });
    } catch (e) {
      // Never crash Pi
    }
  };

  pi.on("agent_start", async (_event, _ctx) => {
    notify("active", "Working");
  });

  pi.on("tool_execution_start", async (_event, _ctx) => {
    notify("active", "Using tool");
  });

  pi.on("tool_execution_end", async (_event, _ctx) => {
    notify("active", "Working");
  });

  pi.on("tool_call", async (event, _ctx) => {
    if (event.requiresConfirmation) {
      notify("waiting", "Permission needed");
    } else {
      notify("active", "Using tool");
    }
  });

  pi.on("agent_end", async (_event, _ctx) => {
    notify("completed", "Task complete");
  });

  pi.on("session_shutdown", async (_event, _ctx) => {
    notify("completed", "Session ended");
  });
}
