package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
	"github.com/mnemoo/cli/internal/mcpserver"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/version"
)

func RunMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sidFlag := fs.String("sid", "", "Stake session credential (prefer STAKE_SID or keyring)")
	keyFlag := fs.String("key", "", "Alias for --sid")
	acceleratorRef := fs.String("accelerator", os.Getenv("STAKE_ACCELERATOR"), "Accelerator profile or IP address")
	acceleratorPassword := fs.String("accelerator-password", "", "Accelerator password (prefer environment)")
	acceleratorPasswordFile := fs.String("accelerator-password-file", "", "Read accelerator password from a protected file")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: stakecli mcp [--sid VALUE|--key VALUE]")
		fmt.Fprintln(os.Stderr, "\nRuns the native MCP server over stdio. Logs are written only to stderr.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *sidFlag != "" && *keyFlag != "" {
		return errors.New("use only one of --sid or --key")
	}

	sid := *sidFlag
	source := ""
	if sid != "" {
		source = "--sid"
	}
	if sid == "" && *keyFlag != "" {
		sid = *keyFlag
		source = "--key"
	}
	if sid == "" {
		var err error
		sid, err = auth.GetActiveSID()
		if err == nil {
			if os.Getenv("STAKE_SID") != "" {
				source = "STAKE_SID"
			} else {
				source = "keyring"
			}
		}
	}
	if sid != "" && (source == "--sid" || source == "--key") {
		previousSID, hadPreviousSID := os.LookupEnv("STAKE_SID")
		if err := os.Setenv("STAKE_SID", sid); err != nil {
			return fmt.Errorf("preparing daemon authentication: %w", err)
		}
		defer func() {
			if hadPreviousSID {
				_ = os.Setenv("STAKE_SID", previousSID)
			} else {
				_ = os.Unsetenv("STAKE_SID")
			}
		}()
	}

	client := api.NewClient(sid)
	appService := service.New(client)
	if *acceleratorRef != "" {
		if *acceleratorPassword == "" && *acceleratorPasswordFile == "" && os.Getenv("STAKE_ACCELERATOR_PASSWORD") == "" {
			return errors.New("MCP accelerator requires STAKE_ACCELERATOR_PASSWORD, --accelerator-password, or --accelerator-password-file")
		}
		secret, err := readAcceleratorPassword(*acceleratorPassword, *acceleratorPasswordFile)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		relay, _, err := accelerator.ConnectConfigured(ctx, *acceleratorRef, secret)
		if err != nil {
			return fmt.Errorf("connecting accelerator: %w", err)
		}
		appService = service.NewWithUploadClient(client, &accelerator.UploadClient{
			Client: client, Accelerator: relay,
		})
	}
	server := mcpserver.New(appService, mcpserver.AuthInfo{
		Configured: sid != "",
		Source:     source,
	}, version.Short())
	return server.MCP().Run(context.Background(), &mcp.StdioTransport{})
}
