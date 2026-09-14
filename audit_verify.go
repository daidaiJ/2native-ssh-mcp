package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// runAuditVerify implements the audit-verify subcommand: recompute the hash
// chain of one audit file (--file) or of every connection's current plus
// rotated audit files (--config-file). Exit codes follow the CLI convention:
// 0 intact (or no audit log at all), 2 broken chain / legacy format, 1
// program error. There is no repair tool on purpose — a broken chain stays
// broken for human review.
func runAuditVerify(args []string) {
	fs := flag.NewFlagSet("audit-verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "verify a single audit log file (<connection>.jsonl)")
	configFile := fs.String("config-file", "", "verify every connection's audit logs declared in this config file")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() > 0 || (*file == "" && *configFile == "") || (*file != "" && *configFile != "") {
		fmt.Fprintln(os.Stderr, "usage: 2native-ssh-mcp audit-verify --file <path> | --config-file <path>")
		os.Exit(1)
	}

	if *file != "" {
		if _, err := os.Stat(*file); err != nil {
			fmt.Println("no audit log")
			return
		}
		if err := manager.VerifyAuditFiles([]string{*file}); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(2)
		}
		fmt.Printf("audit chain OK: %s\n", *file)
		return
	}

	opts, err := config.ParseArgs([]string{"--config-file", *configFile})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(opts.Configs))
	for name := range opts.Configs {
		names = append(names, name)
	}
	sort.Strings(names)
	checked := 0
	for _, name := range names {
		dir := opts.Configs[name].CommandLogDir
		if dir == "" {
			dir = opts.CommandLogDir
		}
		paths := manager.AuditFilesForConnection(dir, name)
		if len(paths) == 0 {
			continue
		}
		checked++
		if err := manager.VerifyAuditFiles(paths); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(2)
		}
	}
	if checked == 0 {
		fmt.Println("no audit log")
		return
	}
	fmt.Printf("audit chain OK: %d file(s) verified\n", checked)
}
