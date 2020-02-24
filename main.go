package main

import (
	"fmt"
	"path/filepath"

	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrutil/v2"
	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	// defaultTicketFeeLimits is the default byte string for the default
	// fee limits imposed on a ticket.
	defaultTicketFeeLimits = 0x5800

	defaultExpiry         = int32(0)
	requiredConfirmations = 0
	accountNumber         = 0 // default account
	rpcVersion            = "1.0"

	sendTxCmd         = "sendtx"
	purchaseTicketCmd = "purchaseticket"

	// send ticket config
	sourceAccount = 0
	changeAccount = 0
)

var certificateFile = filepath.Join(dcrutil.AppDataDir("dcrwallet", false), "rpc.cert")

func main() {

	cfg, err := loadConfig()
	if err != nil {
		fmt.Println(err)
		return
	}

	conn, err := connect(cfg.GRPCServer)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()

	var activeNet *chaincfg.Params
	if cfg.Network == chaincfg.TestNet3Params().Name {
		activeNet = chaincfg.TestNet3Params()
	} else {
		activeNet = chaincfg.MainNetParams()
	}

	if cfg.PurchaseTicket {

		tb := NewTicketBuyer(cfg, conn, chaincfg.TestNet3Params())

		err = tb.updateFees()
		if err != nil {
			fmt.Println(err)
			return
		}

		err = tb.purchaseTicket()
		if err != nil {
			fmt.Println(err)
			return
		}
	} else {
		walletService := pb.NewWalletServiceClient(conn)
		addr, err := dcrutil.DecodeAddress(cfg.DestinationAddress, activeNet)
		if err != nil {
			fmt.Println(err)
			return
		}

		outputScript, _, err := addressScript(addr)
		if err != nil {
			fmt.Println(err)
			return
		}

		_, changeScript, err := generateAddress(true, cfg.SourceAccount, activeNet, walletService)
		if err != nil {
			fmt.Println(err)
			return
		}

		utxos, err := listUnspentOutputs(cfg)
		if err != nil {
			fmt.Println(err)
			return
		}

		amount, err := dcrutil.NewAmount(cfg.SendAmount)
		if err != nil {
			fmt.Println(err)
			return
		}

		rt := NewRegularTransaction(cfg, outputScript, changeScript, amount, utxos, walletService)
		_, err = rt.broadcastTransaction()
		if err != nil {
			fmt.Println(err)
			return
		}

	}
}

func printUsage() {
	fmt.Printf("Usage:\nticketbuyer %s | %s\n", sendTxCmd, purchaseTicketCmd)
}

func connect(grpcServer string) (*grpc.ClientConn, error) {

	creds, err := credentials.NewClientTLSFromFile(certificateFile, "localhost")
	if err != nil {
		return nil, err
	}

	conn, err := grpc.Dial(grpcServer, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}

	return conn, nil
}
