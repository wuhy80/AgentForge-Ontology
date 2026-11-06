package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func main() {
	tenant := flag.String("tenant", "", "租户 ID")
	subject := flag.String("subject", "", "主体 ID")
	ttl := flag.Duration("ttl", time.Hour, "有效期，最多 24 小时")
	flag.Parse()
	token, err := platform.SignToken(os.Getenv("ONTOLOGY_AUTH_SECRET"), contracts.Principal{
		Subject: *subject, TenantID: *tenant, ExpiresAt: time.Now().Add(*ttl).Unix(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(token)
}
