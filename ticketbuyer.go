package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/decred/dcrd/blockchain/stake/v2"
	"github.com/decred/dcrd/dcrjson/v3"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrwallet/errors/v2"
	wallettypes "github.com/decred/dcrwallet/rpc/jsonrpc/types"
	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrwallet/wallet/v3/txrules"
	"google.golang.org/grpc"
)

const (
	generatedTxVersion uint16 = 1
)

var (
	// fees are in dcr per kb
	ticketFeeRelayDCR dcrutil.Amount
	txRelayFeeDCR     dcrutil.Amount
)

type TicketBuyer struct {
	conn          *grpc.ClientConn
	walletService pb.WalletServiceClient

	cfg *config

	netParams dcrutil.AddressParams
}

func NewTicketBuyer(cfg *config, conn *grpc.ClientConn, netParams dcrutil.AddressParams) *TicketBuyer {

	return &TicketBuyer{
		cfg:           cfg,
		conn:          conn,
		walletService: pb.NewWalletServiceClient(conn),
		netParams:     netParams,
	}
}

func (tb *TicketBuyer) updateFees() error {

	err := tb.updateTicketRelayFee()
	if err != nil {
		return err
	}

	return tb.updateTransactionRelayFee()
}

func (tb *TicketBuyer) printBalance() error {
	ctx := context.Background()

	balanceRequest := &pb.BalanceRequest{
		AccountNumber:         tb.cfg.SourceAccount,
		RequiredConfirmations: requiredConfirmations,
	}
	balanceResponse, err := tb.walletService.Balance(ctx, balanceRequest)
	if err != nil {
		return err
	}
	spendableBal := dcrutil.Amount(balanceResponse.Spendable)
	fmt.Printf("Mixed account spendable balance: %s\n", spendableBal)
	return nil
}

func (tb *TicketBuyer) updateTicketRelayFee() error {
	ticketFeeCmd := wallettypes.NewGetTicketFeeCmd()
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, ticketFeeCmd)
	if err != nil {
		return err
	}

	resp, err := sendPostRequest(tb.cfg.RPCServer, tb.cfg.RPCUser, tb.cfg.RPCPass, marshalledJSON)
	if err != nil {
		return err
	}

	var relayFee float64
	err = json.Unmarshal(resp.Result, &relayFee)
	if err != nil {
		return err
	}

	ticketFeeRelayDCR, err = dcrutil.NewAmount(relayFee)
	if err != nil {
		return err
	}

	return nil
}

func (tb *TicketBuyer) updateTransactionRelayFee() error {
	walletFeeCmd := wallettypes.NewGetWalletFeeCmd()
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, walletFeeCmd)
	if err != nil {
		return err
	}

	resp, err := sendPostRequest(tb.cfg.RPCServer, tb.cfg.RPCUser, tb.cfg.RPCPass, marshalledJSON)
	if err != nil {
		return err
	}

	var relayFee float64
	err = json.Unmarshal(resp.Result, &relayFee)
	if err != nil {
		return err
	}

	txRelayFeeDCR, err = dcrutil.NewAmount(relayFee)
	if err != nil {
		return err
	}

	return nil
}

func (tb *TicketBuyer) listenForBlockNotifications() error {
	ctx := context.Background()
	notifiationClient, err := tb.walletService.TransactionNotifications(ctx, &pb.TransactionNotificationsRequest{})
	if err != nil {
		return err
	}

	fmt.Println("Listening for block notifcations")
	for {
		notificationResponse, err := notifiationClient.Recv()
		if err != nil {
			if err == context.Canceled {
				break
			}

			return err
		}

		ticketPrice, err := tb.getTicketPrice()
		if err != nil {
			return err
		}

		numAttachedBlocks := len(notificationResponse.AttachedBlocks)
		fmt.Printf("%d block(s) attached, Ticket Price: %s\n", numAttachedBlocks, ticketPrice)

		err = tb.purchaseTicket()
		if err != nil {
			return err
		}
	}

	select {}
}

func (tb *TicketBuyer) getTicketPrice() (dcrutil.Amount, error) {
	ctx := context.Background()
	ticketPriceResponse, err := tb.walletService.TicketPrice(ctx, &pb.TicketPriceRequest{})
	if err != nil {
		return 0, err
	}

	return dcrutil.Amount(ticketPriceResponse.TicketPrice), nil
}

func (tb *TicketBuyer) purchaseTicket() error {

	tb.printUnspentOutputs()
	ticketPrice, err := tb.getTicketPrice()
	if err != nil {
		return err
	}

	votingAddress, _, err := generateAddress(true, tb.cfg.VotingAccount, tb.netParams, tb.walletService)
	if err != nil {
		return err
	}

	estTxSize := estimateTicketSize(votingAddress)
	ticketFee := txrules.FeeForSerializeSize(ticketFeeRelayDCR, estTxSize)
	fmt.Printf("Ticket Price: %s, Ticket Fee: %s\n", ticketPrice, ticketFee)
	totalTicketCost := ticketPrice + ticketFee

	fundingTx, err := tb.sendFundingTx(totalTicketCost)
	if err != nil {
		return err
	}

	fmt.Printf("Funding Tx Hash: %s\n", fundingTx.TxHash())

	fundingOutputIndex := -1
	for index, output := range fundingTx.TxOut {
		if output.Value == int64(totalTicketCost) {
			fmt.Printf("Found ticket sized output, Value: %s\n", dcrutil.Amount(output.Value))
			fundingOutputIndex = index
		}
	}

	if fundingOutputIndex == -1 {
		return errors.New("could not find input to fund ticket transaction")
	}

	mtx := wire.NewMsgTx()

	fundingTxHash := fundingTx.TxHash()
	txInOutpoint := wire.NewOutPoint(&fundingTxHash, uint32(fundingOutputIndex), 0)
	txIn := wire.NewTxIn(txInOutpoint, int64(totalTicketCost), []byte{})
	mtx.AddTxIn(txIn)

	fmt.Printf("Total input: %s\n", dcrutil.Amount(txIn.ValueIn))

	sstxPkScript, err := txscript.PayToSStx(votingAddress)
	if err != nil {
		return err
	}
	sstxOut := wire.NewTxOut(int64(ticketPrice), sstxPkScript)
	mtx.AddTxOut(sstxOut)

	fmt.Printf("Total output: %s\n", dcrutil.Amount(sstxOut.Value))

	sstxCommitmentAddr, _, err := generateAddress(true, tb.cfg.ChangeAccount, tb.netParams, tb.walletService)
	if err != nil {
		return err
	}

	sstxCommitmentPkScript, err := txscript.GenerateSStxAddrPush(sstxCommitmentAddr, totalTicketCost, defaultTicketFeeLimits)
	if err != nil {
		return err
	}

	sstxCommitmentTxOut := &wire.TxOut{
		Value:    0,
		PkScript: sstxCommitmentPkScript,
		Version:  0,
	}
	mtx.AddTxOut(sstxCommitmentTxOut)

	sstxChangeAddr, _, err := generateAddress(true, tb.cfg.ChangeAccount, tb.netParams, tb.walletService)
	if err != nil {
		return err
	}

	sstxChangeScript, err := txscript.PayToSStxChange(sstxChangeAddr)
	if err != nil {
		return err
	}
	sstxChangeTxOut := &wire.TxOut{
		Value:    0,
		PkScript: sstxChangeScript,
		Version:  0,
	}

	mtx.AddTxOut(sstxChangeTxOut)

	if err = stake.CheckSStx(mtx); err != nil {
		fmt.Printf("Error generate ticket transaction: %v\n", err)
		return err
	}

	serializedTx, err := mtx.Bytes()
	if err != nil {
		return err
	}

	hash, err := signAndPublishTransaction(tb.cfg.WalletPassphrase, serializedTx, tb.walletService)
	if err != nil {
		return err
	}

	fmt.Printf("Tx Hash: %s\n", hash.String())

	return nil
}

func (tb *TicketBuyer) printUnspentOutputs() error {

	unspentOutputs, err := listUnspentOutputs(tb.cfg)
	if err != nil {
		return err
	}

	fmt.Println("Unspent Outputs")
	for _, unspentOutput := range unspentOutputs {
		fmt.Printf("%s:%d Spendable: %t Account: %s, Amount: %f DCR\n", unspentOutput.TxID, unspentOutput.Vout,
			unspentOutput.Spendable, unspentOutput.Account, unspentOutput.Amount)
	}

	return nil
}

func (tb *TicketBuyer) sendFundingTx(totalTicketCost dcrutil.Amount) (*wire.MsgTx, error) {

	_, outputScript, err := generateAddress(true, tb.cfg.SourceAccount, tb.netParams, tb.walletService)
	if err != nil {
		return nil, err
	}

	_, changeScript, err := generateAddress(true, tb.cfg.SourceAccount, tb.netParams, tb.walletService)
	if err != nil {
		return nil, err
	}

	utxos, err := listUnspentOutputs(tb.cfg)
	if err != nil {
		return nil, err
	}

	regularTx := NewRegularTransaction(tb.cfg, outputScript, changeScript, totalTicketCost, utxos, tb.walletService)
	return regularTx.broadcastTransaction()
}
