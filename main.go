package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// stdout é reservado ao protocolo MCP; todo log vai para stderr.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	log.SetPrefix("pluggy-mcp: ")

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	client := NewClient(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check":
			if err := runCheck(ctx, client, cfg.ItemIDs); err != nil {
				log.Fatal(err)
			}
			return
		default:
			log.Fatalf("subcomando desconhecido %q (use \"check\" ou nenhum argumento para rodar o servidor MCP)", os.Args[1])
		}
	}

	server := newMCPServer(client, cfg.ItemIDs)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
