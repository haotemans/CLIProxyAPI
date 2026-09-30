package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/appserver"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/adminreset"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/cpaconnection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/derivedmaintenance"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/runtimeconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/usagecompact"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
)

func main() {
	if handled, err := writeVersion(os.Args[1:], os.Stdout); handled {
		if err != nil {
			log.Printf("write version: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin-key", "reset-admin-password":
			if err := adminreset.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("reset admin key: %v", err)
				os.Exit(1)
			}
			return
		case "cleanup-derived":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := derivedmaintenance.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("cleanup derived data: %v", err)
				os.Exit(1)
			}
			return
		case "compact-usage":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := usagecompact.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				if usagecompact.IsHelp(err) {
					return
				}
				log.Printf("compact usage database: %v", err)
				os.Exit(1)
			}
			return
		case "store-cpa-connection":
			if err := cpaconnection.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("store CPA connection: %v", err)
				os.Exit(1)
			}
			return
		case "manager-data-snapshot":
			if err := runManagerDataSnapshotCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("manage Manager data snapshot: %v", err)
				os.Exit(1)
			}
			return
		case "sanitize-runtime-config":
			if err := runtimeconfig.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("sanitize runtime config: %v", err)
				os.Exit(1)
			}
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := appserver.RunServer(ctx, cfg); err != nil {
		log.Fatalf("manager server: %v", err)
	}
}

func writeVersion(args []string, stdout io.Writer) (bool, error) {
	if len(args) != 1 || (args[0] != "-v" && args[0] != "--version") {
		return false, nil
	}
	_, err := fmt.Fprintln(stdout, buildinfo.Version)
	return true, err
}

func runManagerDataSnapshotCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return managerdatasnapshot.Run(ctx, args, stdout, stderr)
}
