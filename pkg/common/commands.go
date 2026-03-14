package common

import (
	"github.com/urfave/cli/v3"
)

var commands = make(map[string][]*cli.Command, 0)

// RegisterCommand allows you to register a command under the main group
func RegisterCommand(command *cli.Command) {
	commands["_main_"] = append(commands["_main_"], command)
}

// GetCommands retrieves all commands assigned to the main group
func GetCommands() []*cli.Command {
	return commands["_main_"]
}
