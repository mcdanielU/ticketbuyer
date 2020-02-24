package main

import (
	"errors"
	"fmt"
	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrutil/v2"
	"os"

	flags "github.com/jessevdk/go-flags"
)

const (
	defaultNetwork = "testnet3"

	defaultSourceAccount uint32 = 0 // mixed account
	defaultChangeAccount uint32 = 1 // unmixed account
	defaultVotingAccount uint32 = 2

	defaultGRPCServer    = "localhost:19111"
	defaultGPRCPort      = "19111"
	defaultJSONRPCServer = "localhost:19110"
	defaultJSONRPCPort   = "19110"
	defaultRPCUser       = "dcrwallet"
	defaultRPCPass       = "dcrwallet"
)

type config struct {
	Network            string  `long:"network" description:"specify network to use"`
	SendTx             bool    `long:"sendtx" description:"send regular transaction using randomixed utxos"`
	DestinationAddress string  `long:"destaddr" description:"must be used with --sendtx"`
	SendAmount         float64 `long:"amount" description:"must be used with --sendtx"`
	PurchaseTicket     bool    `long:"purchaseticket"`
	SpendUnconfirmed   bool    `long:"spendunconfirmed" description:"allow use of unconfirmed utxos"`
	SourceAccountName  string  `long:"sourceaccountname" description:"account name for same account passed as --sourceaccount"`
	SourceAccount      uint32  `long:"sourceaccount" description:"account used to send funds using randomized inputs and also used to derive fresh addresses from for mixed ticket splits"`
	ChangeAccount      uint32  `long:"changeaccount" description:"account used as change output in regular transactions and also used to derive unmixed CoinJoin outputs"`
	VotingAccount      uint32  `long:"votingaccount" description:"account used to derive addresses specifying voting rights"`
	GRPCServer         string  `long:"grpcserver" description:"Wallet GRPC server to connect to"`
	RPCServer          string  `long:"rpcserver" description:"Wallet RPC server to connect to"`
	RPCUser            string  `long:"rpcuser" description:"JSON-RPC username and default dcrwallet GRPC username"`
	RPCPass            string  `long:"rpcPass" description:"JSON-RPC password and default dcrwallet GRPC password"`
	WalletPassphrase   string  `long:"walletpass" description:"Wallet passphrase"`
}

var defaultConfig = config{
	Network:           defaultNetwork,
	SourceAccountName: "default",
	SourceAccount:     defaultSourceAccount,
	ChangeAccount:     defaultChangeAccount,
	VotingAccount:     defaultVotingAccount,
	RPCUser:           defaultRPCUser,
	RPCPass:           defaultRPCPass,
	GRPCServer:        defaultGRPCServer,
	RPCServer:         defaultJSONRPCServer,
}

// loadConfig initializes and parses the config using a config file and command
// line options.
func loadConfig() (*config, error) {
	loadConfigError := func(err error) (*config, error) {
		return nil, err
	}

	// // Default config
	cfg := defaultConfig

	parser := flags.NewParser(&cfg, flags.HelpFlag|flags.PassDoubleDash)
	_, flagerr := parser.Parse()

	if flagerr != nil {
		e, ok := flagerr.(*flags.Error)
		if !ok || e.Type != flags.ErrHelp {
			parser.WriteHelp(os.Stderr)
		}
		if ok && e.Type == flags.ErrHelp {
			parser.WriteHelp(os.Stdout)
			os.Exit(0)
		}
		return loadConfigError(flagerr)
	}

	actionError := errors.New("Specify either --sendtx or --purchaseticket")
	if cfg.PurchaseTicket == cfg.SendTx { // both can't be false or true
		return loadConfigError(actionError)
	}

	var activeNet *chaincfg.Params
	if cfg.Network == chaincfg.TestNet3Params().Name {
		activeNet = chaincfg.TestNet3Params()
	} else if cfg.Network != chaincfg.MainNetParams().Name {
		activeNet = chaincfg.MainNetParams()
	} else {
		return loadConfigError(fmt.Errorf("network must be testnet3 or mainnet"))
	}

	var err error
	cfg.GRPCServer, err = NormalizeAddress(cfg.GRPCServer, defaultGPRCPort)
	if err != nil {
		return loadConfigError(fmt.Errorf("invalid grpc server address: %v", err))
	}

	cfg.RPCServer, err = NormalizeAddress(cfg.RPCServer, defaultJSONRPCPort)
	if err != nil {
		return loadConfigError(fmt.Errorf("invalid json-rpc server address: %v", err))
	}

	if cfg.SourceAccountName == "" {
		return loadConfigError(fmt.Errorf("source account name must be set"))
	}

	if cfg.WalletPassphrase == "" {
		return loadConfigError(fmt.Errorf("wallet passphrase must be set"))
	}

	if cfg.SendTx {
		if cfg.DestinationAddress == "" {
			return loadConfigError(fmt.Errorf("destination address must be set when using --sendtx"))
		}

		_, err = dcrutil.DecodeAddress(cfg.DestinationAddress, activeNet)
		if err != nil {
			return loadConfigError(fmt.Errorf("decode destaddr error: %v", err))
		}

		if cfg.SendAmount <= 0 {
			return loadConfigError(fmt.Errorf("amount must be a >0"))
		} else {
			_, err = dcrutil.NewAmount(cfg.SendAmount)
			if err != nil {
				return loadConfigError(fmt.Errorf("amount error: %v", err))
			}
		}
	}

	return &cfg, nil
}
