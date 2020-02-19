package main

import (
	"fmt"
	"path/filepath"

	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrutil/v2"
)

const (
	// defaultTicketFeeLimits is the default byte string for the default
	// fee limits imposed on a ticket.
	defaultTicketFeeLimits = 0x5800

	defaultExpiry         = int32(0)
	requiredConfirmations = 0
	accountNumber         = 0 // default account
	rpcVersion            = "1.0"
	grpcServer            = "localhost:19111"
)

func main() {

	certificateFile := filepath.Join(dcrutil.AppDataDir(rpcUser, false), "rpc.cert")

	tb := NewTicketBuyer(grpcServer, certificateFile, chaincfg.TestNet3Params())

	err := tb.connect()
	if err != nil {
		fmt.Println(err)
		return
	}

	err = tb.purchaseTicket()
	if err != nil {
		fmt.Println(err)
		return
	}
}
