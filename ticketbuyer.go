package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"io/ioutil"
	"net"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/decred/dcrd/blockchain/stake/v2"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrjson/v3"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrwallet/errors/v2"
	wallettypes "github.com/decred/dcrwallet/rpc/jsonrpc/types"
	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrwallet/wallet/v3/txauthor"
	"github.com/decred/dcrwallet/wallet/v3/txrules"
	"github.com/decred/dcrwallet/wallet/v3/txsizes"
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
	host            string
	certificateFile string
	conn            *grpc.ClientConn
	walletService   pb.WalletServiceClient

	netParams dcrutil.AddressParams
}

func NewTicketBuyer(host, certificateFile string, netParams dcrutil.AddressParams) *TicketBuyer {

	return &TicketBuyer{
		host:            host,
		certificateFile: certificateFile,
		netParams:       netParams,
	}
}

func (tb *TicketBuyer) connect() error {

	if tb.conn != nil {
		return errors.New("connection exitss")
	}

	creds, err := credentials.NewClientTLSFromFile(tb.certificateFile, tb.host)
	if err != nil {
		return err
	}

	conn, err := grpc.Dial("localhost:19111", grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	walletService := pb.NewWalletServiceClient(conn)

	tb.conn = conn
	tb.walletService = walletService

	err = tb.updateTicketRelayFee()
	if err != nil {
		return err
	}

	return tb.updateTransactionRelayFee()
}

func (tb *TicketBuyer) disconnect() error {
	if tb.conn == nil {
		return errors.New("no active connection")
	}
	return tb.conn.Close()
}

func (tb *TicketBuyer) printBalance() error {
	ctx := context.Background()

	balanceRequest := &pb.BalanceRequest{
		AccountNumber:         0,
		RequiredConfirmations: requiredConfirmations,
	}
	balanceResponse, err := tb.walletService.Balance(ctx, balanceRequest)
	if err != nil {
		return err
	}
	spendableBal := dcrutil.Amount(balanceResponse.Spendable)
	fmt.Println("Spendable balance:", spendableBal)
	return nil
}

func (tb *TicketBuyer) updateTicketRelayFee() error {
	ticketFeeCmd := wallettypes.NewGetTicketFeeCmd()
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, ticketFeeCmd)
	if err != nil {
		return err
	}

	resp, err := tb.sendPostRequest(marshalledJSON)
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

	resp, err := tb.sendPostRequest(marshalledJSON)
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

func (tb *TicketBuyer) generateAddress(internal bool) (address dcrutil.Address, pkScript []byte, err error) {
	ctx := context.Background()
	addressRequest := &pb.NextAddressRequest{
		Account:   0,
		Kind:      pb.NextAddressRequest_BIP0044_EXTERNAL,
		GapPolicy: pb.NextAddressRequest_GAP_POLICY_WRAP,
	}

	if internal {
		addressRequest.Kind = pb.NextAddressRequest_BIP0044_INTERNAL
	}

	addressResponse, err := tb.walletService.NextAddress(ctx, addressRequest)
	if err != nil {
		return
	}

	address, err = dcrutil.DecodeAddress(addressResponse.Address, tb.netParams)
	if err != nil {
		return
	}

	pkScript, _, err = addressScript(address)
	return
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

	votingAddress, _, err := tb.generateAddress(true)
	if err != nil {
		return err
	}

	estTxSize := estimateTicketSize(votingAddress)
	ticketFee := txrules.FeeForSerializeSize(ticketFeeRelayDCR, estTxSize)
	fmt.Printf("Ticket Fee: %s\n", ticketFee)
	totalTicketCost := ticketPrice + ticketFee

	fundingTx, err := tb.sendFundingTx(totalTicketCost)
	if err != nil {
		return err
	}

	fmt.Printf("Funding Tx Hash: %s\n", fundingTx.TxHash())

	fundingOutputIndex := -1
	for index, output := range fundingTx.TxOut {
		if output.Value == int64(totalTicketCost) {
			fmt.Printf("Output Value: %s, Cost Equal: %v\n", dcrutil.Amount(output.Value), output.Value == int64(totalTicketCost))
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

	fmt.Printf("Value in: %s\n", dcrutil.Amount(txIn.ValueIn))

	sstxPkScript, err := txscript.PayToSStx(votingAddress)
	if err != nil {
		return err
	}
	sstxOut := wire.NewTxOut(int64(ticketPrice), sstxPkScript)
	mtx.AddTxOut(sstxOut)

	fmt.Printf("Value out: %s\n", dcrutil.Amount(sstxOut.Value))

	sstxCommitmentAddr, _, err := tb.generateAddress(true)
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

	sstxChangeAddr, _, err := tb.generateAddress(true)
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

	hash, err := tb.signAndPublishTransaction(serializedTx)
	if err != nil {
		return err
	}

	fmt.Printf("Tx Hash: %s", hash.String())

	return nil
}

func (tb *TicketBuyer) listUnspentOutputs() ([]wallettypes.ListUnspentResult, error) {
	minConfs := requiredConfirmations
	unspentCmd := wallettypes.NewListUnspentCmd(&minConfs, nil, nil)
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, unspentCmd)
	if err != nil {
		return nil, err
	}

	resp, err := tb.sendPostRequest(marshalledJSON)
	if err != nil {
		return nil, err
	}

	var unspentOutputs []wallettypes.ListUnspentResult
	err = json.Unmarshal(resp.Result, &unspentOutputs)
	if err != nil {
		return nil, err
	}

	return unspentOutputs, nil
}

func (tb *TicketBuyer) printUnspentOutputs() error {

	unspentOutputs, err := tb.listUnspentOutputs()
	if err != nil {
		return err
	}

	fmt.Println("Unspent Outputs")
	for _, unspentOutput := range unspentOutputs {
		fmt.Printf("%s:%d Spendable: %t Amount: %f DCR\n", unspentOutput.TxID, unspentOutput.Vout, unspentOutput.Spendable, unspentOutput.Amount)
	}

	return nil
}

func (tb *TicketBuyer) selectInputsForAmount(targetAmount dcrutil.Amount) (*txauthor.InputDetail, error) {
	unspentOutputs, err := tb.listUnspentOutputs()
	if err != nil {
		return nil, err
	}

	var (
		currentTotal      dcrutil.Amount
		currentInputs     []*wire.TxIn
		currentScripts    [][]byte
		redeemScriptSizes []int
	)

	for _, unspentOutput := range unspentOutputs {
		if unspentOutput.Spendable {
			unspentOutputAmount, err := dcrutil.NewAmount(unspentOutput.Amount)
			if err != nil {
				return nil, err
			}

			txHash, err := chainhash.NewHashFromStr(unspentOutput.TxID)
			if err != nil {
				return nil, err
			}

			txInOutpoint := wire.NewOutPoint(txHash, unspentOutput.Vout, unspentOutput.Tree)
			txIn := wire.NewTxIn(txInOutpoint, int64(unspentOutputAmount), nil)

			pkScript, err := hex.DecodeString(unspentOutput.ScriptPubKey)
			if err != nil {
				return nil, err
			}

			scriptClass := txscript.GetScriptClass(0, pkScript)
			var scriptSize int

			switch scriptClass {
			case txscript.PubKeyHashTy:
				scriptSize = txsizes.RedeemP2PKHSigScriptSize
			case txscript.PubKeyTy:
				scriptSize = txsizes.RedeemP2PKSigScriptSize
			case txscript.StakeRevocationTy, txscript.StakeSubChangeTy, txscript.StakeGenTy:
				scriptClass, err = txscript.GetStakeOutSubclass(pkScript)
				if err != nil {
					return nil, errors.Errorf(
						"failed to extract nested script in stake output: %v",
						err)
				}

				// For stake transactions we expect P2PKH and P2SH script class
				// types only but ignore P2SH script type since it can pay
				// to any script which the wallet may not recognize.
				if scriptClass != txscript.PubKeyHashTy {
					fmt.Printf("unexpected nested script class for credit: %v\n",
						scriptClass)
					continue
				}

				scriptSize = txsizes.RedeemP2PKHSigScriptSize
			default:
				fmt.Printf("unexpected script class for credit: %v\n",
					scriptClass)
				continue
			}

			currentTotal += unspentOutputAmount
			currentInputs = append(currentInputs, txIn)
			currentScripts = append(currentScripts, pkScript)
			redeemScriptSizes = append(redeemScriptSizes, scriptSize)

			if currentTotal >= targetAmount {
				return &txauthor.InputDetail{
					Amount:            currentTotal,
					Inputs:            currentInputs,
					Scripts:           currentScripts,
					RedeemScriptSizes: redeemScriptSizes,
				}, nil
			}
		}
	}

	return nil, errors.E(errors.InsufficientBalance)
}

func (tb *TicketBuyer) sendFundingTx(ticketCost dcrutil.Amount) (*wire.MsgTx, error) {

	mtx := wire.NewMsgTx()

	_, outputScript, err := tb.generateAddress(true)
	if err != nil {
		return nil, err
	}
	txOut := wire.NewTxOut(int64(ticketCost), outputScript)
	mtx.AddTxOut(txOut)

	_, changeAddressScript, err := tb.generateAddress(true)
	if err != nil {
		return nil, err
	}
	changeScriptSize := txsizes.P2PKHPkScriptSize

	// init'd with a single script for inital tx fee estimation
	scriptSizes := []int{txsizes.RedeemP2PKHSigScriptSize}

	maxSignedSize := txsizes.EstimateSerializeSize(scriptSizes, mtx.TxOut, changeScriptSize)
	targetFee := txrules.FeeForSerializeSize(txRelayFeeDCR, maxSignedSize)

	for {
		inputDetail, err := tb.selectInputsForAmount(ticketCost + targetFee)
		if err != nil {
			return nil, err
		}

		if inputDetail.Amount < ticketCost+targetFee {
			return nil, errors.E(errors.InsufficientBalance)
		}

		scriptSizes := make([]int, 0, len(inputDetail.RedeemScriptSizes))
		scriptSizes = append(scriptSizes, inputDetail.RedeemScriptSizes...)

		maxSignedSize = txsizes.EstimateSerializeSize(scriptSizes, mtx.TxOut, changeScriptSize)
		maxRequiredFee := txrules.FeeForSerializeSize(txRelayFeeDCR, maxSignedSize)
		remainingAmount := inputDetail.Amount - ticketCost
		if remainingAmount < maxRequiredFee {
			targetFee = maxRequiredFee
			continue
		}

		mtx.TxIn = inputDetail.Inputs
		mtx.SerType = wire.TxSerializeFull
		mtx.Version = generatedTxVersion

		changeAmount := inputDetail.Amount - ticketCost - maxRequiredFee
		if changeAmount != 0 && !txrules.IsDustAmount(changeAmount, changeScriptSize, txRelayFeeDCR) {

			if len(changeAddressScript) > txscript.MaxScriptElementSize {
				return nil, errors.E(errors.Invalid, "script size exceed maximum bytes "+
					"pushable to the stack")
			}

			change := &wire.TxOut{
				Value:    int64(changeAmount),
				PkScript: changeAddressScript,
			}

			mtx.AddTxOut(change)
		}

		serializedTx, err := mtx.Bytes()
		if err != nil {
			return nil, err
		}

		hash, err := tb.signAndPublishTransaction(serializedTx)
		if err != nil {
			return nil, err
		}

		fmt.Printf("Published Funding Tx: %s\n", hash)

		return mtx, nil
	}

}

func (tb *TicketBuyer) signAndPublishTransaction(serializedTx []byte) (hash *chainhash.Hash, err error) {
	ctx := context.Background()
	signTransactionRequest := &pb.SignTransactionRequest{
		Passphrase:            []byte("c"),
		SerializedTransaction: serializedTx,
	}

	signTransactionResponse, err := tb.walletService.SignTransaction(ctx, signTransactionRequest)
	if err != nil {
		return
	}

	publishTransactionRequest := &pb.PublishTransactionRequest{
		SignedTransaction: signTransactionResponse.Transaction,
	}

	publishTransactionResponse, err := tb.walletService.PublishTransaction(ctx, publishTransactionRequest)
	if err != nil {
		return
	}

	fmt.Println("Transaction published")

	hash, err = chainhash.NewHash(publishTransactionResponse.TransactionHash)
	if err != nil {
		return
	}

	return
}

func (tb *TicketBuyer) sendPostRequest(marshalledJSON []byte) (*dcrjson.Response, error) {
	url := "https://127.0.0.1:19110"

	bodyReader := bytes.NewReader(marshalledJSON)
	req, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("dcrwallet", "dcrwallet")

	client, err := tb.newHTTPClient()
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 200 {
		var resp dcrjson.Response
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}

		return &resp, nil
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%d %s", resp.StatusCode,
			http.StatusText(resp.StatusCode))
	}

	return nil, fmt.Errorf("%s", body)
}

func (tb *TicketBuyer) newHTTPClient() (*http.Client, error) {

	// Configure tls
	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
	}

	pem, err := ioutil.ReadFile(tb.certificateFile)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pem); !ok {
		return nil, fmt.Errorf("invalid certificate file: %v", tb.certificateFile)
	}
	tlsConfig.RootCAs = pool

	// Create and return the new HTTP client potentially configured with a
	// proxy and TLS.
	var dial func(network, addr string) (net.Conn, error)
	client := http.Client{
		Transport: &http.Transport{
			Dial:            dial,
			TLSClientConfig: tlsConfig,
		},
	}
	return &client, nil
}
