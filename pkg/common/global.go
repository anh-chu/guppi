package common

import (
	"context"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "log-level",
			Usage:   "Log Level",
			Aliases: []string{"l"},
			Sources: cli.EnvVars("LOG_LEVEL"),
			Value:   "info",
		},
		&cli.BoolFlag{
			Name:    "log-caller",
			Usage:   "log the caller (aka line number and file)",
			Sources: cli.EnvVars("LOG_CALLER"),
			Value:   false,
		},
		&cli.StringFlag{
			Name:    "log-format",
			Usage:   "the log format to use (auto, json, console)",
			Sources: cli.EnvVars("LOG_FORMAT"),
			Value:   "auto",
		},
	}
}

func Before(ctx context.Context, c *cli.Command) (context.Context, error) {
	logLevel := c.String("log-level")
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return ctx, err
	}

	logrus.SetLevel(level)

	if c.String("log-format") == "json" || (!term.IsTerminal(int(os.Stdout.Fd())) && c.String("log-format") == "auto") {
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05Z07:00",
		})
	} else {
		logrus.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "15:04:05",
		})
	}

	logrus.SetOutput(os.Stdout)

	if c.Bool("log-caller") {
		logrus.SetReportCaller(true)
	}

	return ctx, nil
}
