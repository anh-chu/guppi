package server

import "embed"

//go:embed dist/*
var frontendFS embed.FS
