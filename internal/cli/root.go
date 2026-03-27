package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/garett/mailr/internal/api"
	"github.com/garett/mailr/internal/db"
	"github.com/garett/mailr/internal/relay"
	smtpserver "github.com/garett/mailr/internal/smtp"
	"github.com/garett/mailr/internal/store"
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mailr",
		Short: "Mail relay for AI agents",
	}

	root.AddCommand(newServeCmd())
	root.AddCommand(newSetupCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newManageCmd())

	return root
}

func newServeCmd() *cobra.Command {
	var (
		dbPath   string
		httpAddr string
		smtpAddr string
		domain   string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the mail relay server",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := db.Open(dbPath)
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer database.Close()

			st, err := store.NewStore(database)
			if err != nil {
				return fmt.Errorf("initializing store: %w", err)
			}

			smtp := smtpserver.NewServer(st, domain, smtpAddr)
			go func() {
				if err := smtp.ListenAndServe(); err != nil {
					fmt.Fprintf(os.Stderr, "SMTP server error: %v\n", err)
				}
			}()

			r := relay.New(st, domain)
			queueStop := make(chan struct{})
			go r.StartQueueProcessor(queueStop)

			apiSrv := api.NewServer(st)

			fmt.Printf("mailr serving:\n")
			fmt.Printf("  HTTP API: %s\n", httpAddr)
			fmt.Printf("  SMTP:     %s\n", smtpAddr)
			fmt.Printf("  Domain:   %s\n", domain)

			go func() {
				if err := http.ListenAndServe(httpAddr, apiSrv.Router); err != nil && err != http.ErrServerClosed {
					fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
					os.Exit(1)
				}
			}()

			quit := make(chan os.Signal, 1)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			<-quit
			fmt.Println("\nShutting down...")
			close(queueStop)
			smtp.Close()
			return nil
		},
	}

	cmd.Flags().StringVar(&dbPath, "db", "mailr.db", "Database path")
	cmd.Flags().StringVar(&httpAddr, "http", ":4802", "HTTP API listen address")
	cmd.Flags().StringVar(&smtpAddr, "smtp", ":2525", "SMTP listen address")
	cmd.Flags().StringVar(&domain, "domain", "localhost", "Server domain name")

	return cmd
}
