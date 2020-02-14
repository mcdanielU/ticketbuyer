package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"path/filepath"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/decred/dcrd/blockchain/stake/v2"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrjson/v3"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrd/wire"
	wallettypes "github.com/decred/dcrwallet/rpc/jsonrpc/types"
	"github.com/decred/dcrwallet/wallet/v3/txrules"

	pb "github.com/decred/dcrwallet/rpc/walletrpc"
)

const (
	// defaultTicketFeeLimits is the default byte string for the default
	// fee limits imposed on a ticket.
	defaultTicketFeeLimits = 0x5800

	numTickets            = 1
	defaultExpiry         = int32(0)
	requiredConfirmations = 0
	accountNumber         = 0 // default account
	rpcVersion            = "1.0"
)

var (
	ticketFeeRelayDCR dcrutil.Amount
	votingAddress     = ""
	netParams         = chaincfg.TestNet3Params()
	conn              *grpc.ClientConn
)

var certificateFile = filepath.Join(dcrutil.AppDataDir("dcrwallet", false), "rpc.cert")

func main() {
	creds, err := credentials.NewClientTLSFromFile(certificateFile, "localhost")
	if err != nil {
		fmt.Println(err)
		return
	}

	conn, err = grpc.Dial("localhost:19111", grpc.WithTransportCredentials(creds))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()
	walletService := pb.NewWalletServiceClient(conn)

	ctx := context.Background()

	balanceRequest := &pb.BalanceRequest{
		AccountNumber:         0,
		RequiredConfirmations: requiredConfirmations,
	}
	balanceResponse, err := walletService.Balance(ctx, balanceRequest)
	if err != nil {
		fmt.Println(err)
		return
	}
	spendableBal := dcrutil.Amount(balanceResponse.Spendable)
	fmt.Println("Spendable balance:", spendableBal)

	votingAddress, err := generateAddress(ctx, walletService, true)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Send some DCR to:", votingAddress)

	err = updateTicketFee(ctx, walletService)
	if err != nil {
		fmt.Println(err)
		return
	}

	ticketPrice, err := getTicketPrice(ctx, walletService)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Ticket Price: %s\n", ticketPrice)

	err = rawTx(ctx, walletService)
	if err != nil {
		fmt.Println(err)
	}
}

func listenForBlockNotifications(ctx context.Context, walletService pb.WalletServiceClient) error {
	notifiationClient, err := walletService.TransactionNotifications(ctx, &pb.TransactionNotificationsRequest{})
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

		ticketPrice, err := getTicketPrice(ctx, walletService)
		if err != nil {
			return err
		}

		numAttachedBlocks := len(notificationResponse.AttachedBlocks)
		fmt.Printf("%d block(s) attached, Ticket Price: %s\n", numAttachedBlocks, ticketPrice)

		err = purchaseTicket(ctx, walletService)
		if err != nil {
			return err
		}
	}

	select {}
}

func generateAddress(ctx context.Context, walletService pb.WalletServiceClient, internal bool) (dcrutil.Address, error) {
	addressRequest := &pb.NextAddressRequest{
		Account:   0,
		Kind:      pb.NextAddressRequest_BIP0044_EXTERNAL,
		GapPolicy: pb.NextAddressRequest_GAP_POLICY_WRAP,
	}

	if internal {
		addressRequest.Kind = pb.NextAddressRequest_BIP0044_INTERNAL
	}

	addressResponse, err := walletService.NextAddress(ctx, addressRequest)
	if err != nil {
		return nil, err
	}

	decoded, err := dcrutil.DecodeAddress(addressResponse.Address, netParams)
	if err != nil {
		return nil, err
	}

	return decoded, nil
}

func updateTicketFee(ctx context.Context, walletService pb.WalletServiceClient) error {
	ticketFeeCmd := wallettypes.NewGetTicketFeeCmd()
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, ticketFeeCmd)
	if err != nil {
		return err
	}

	resp, err := sendPostRequest(marshalledJSON)
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

func getTicketPrice(ctx context.Context, walletService pb.WalletServiceClient) (dcrutil.Amount, error) {
	ticketPriceResponse, err := walletService.TicketPrice(ctx, &pb.TicketPriceRequest{})
	if err != nil {
		return 0, err
	}

	return dcrutil.Amount(ticketPriceResponse.TicketPrice), nil
}

func purchaseTicket(ctx context.Context, walletService pb.WalletServiceClient) error {
	pruchaseTicketRequest := &pb.PurchaseTicketsRequest{
		NumTickets:            1,
		Account:               0,
		RequiredConfirmations: requiredConfirmations,
		Passphrase:            []byte("c"),
	}

	pruchaseTicketResponse, err := walletService.PurchaseTickets(ctx, pruchaseTicketRequest)
	if err != nil {
		return err
	}

	ticketHash := pruchaseTicketResponse.TicketHashes[0]
	hash, err := chainhash.NewHash(ticketHash)
	if err != nil {
		return err
	}

	fmt.Printf("Purchased ticket: %s\n", hash)
	return nil
}

func rawTx(ctx context.Context, walletService pb.WalletServiceClient) error {

	printUnspentInputs(ctx)

	ticketPrice, err := getTicketPrice(ctx, walletService)
	if err != nil {
		return err
	}

	votingAddress, err := generateAddress(ctx, walletService, true)
	if err != nil {
		return err
	}

	estTxSize := estimateTicketSize(votingAddress)
	ticketFee := txrules.FeeForSerializeSize(ticketFeeRelayDCR, estTxSize)
	fmt.Printf("Ticket Fee: %s\n", ticketFee)
	totalTicketCost := ticketPrice + ticketFee

	fundingTx, err := sendFundingTx(ctx, walletService, totalTicketCost)
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
		return errors.New("Could not find input to fund ticket transaction")
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

	sstxCommitmentAddr, err := generateAddress(ctx, walletService, true)
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

	sstxChangeAddr, err := generateAddress(ctx, walletService, true)
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

	hash, err := signAndPublishTransaction(ctx, walletService, serializedTx)
	if err != nil {
		return err
	}

	fmt.Printf("Tx Hash: %s", hash.String())

	return nil
}

func printUnspentInputs(ctx context.Context) error {
	minConfs := requiredConfirmations
	unspentCmd := wallettypes.NewListUnspentCmd(&minConfs, nil, nil)
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, unspentCmd)
	if err != nil {
		return err
	}

	resp, err := sendPostRequest(marshalledJSON)
	if err != nil {
		return err
	}

	var unspentInputs []wallettypes.ListUnspentResult
	err = json.Unmarshal(resp.Result, &unspentInputs)
	if err != nil {
		fmt.Println("Erroring here")
		return err
	}

	fmt.Println("Unspent inputs")
	for _, unspentInput := range unspentInputs {
		fmt.Printf("%s:%d Spendable: %t Amount: %f DCR\n", unspentInput.TxID, unspentInput.Vout, unspentInput.Spendable, unspentInput.Amount)
	}

	return nil
}

func estimateTicketSize(votingAddress dcrutil.Address) int {

	inSizes := []int{RedeemP2PKHSigScriptSize}

	outSizes := []int{P2PKHPkScriptSize + 1,
		TicketCommitmentScriptSize, P2PKHPkScriptSize + 1}

	estSize := EstimateSerializeSizeFromScriptSizes(inSizes, outSizes, 0)

	fmt.Printf("Estimated Size: %d\n", estSize)

	return estSize
}

func sendFundingTx(ctx context.Context, walletService pb.WalletServiceClient, ticketCost dcrutil.Amount) (*wire.MsgTx, error) {

	fundingTxAddress, err := generateAddress(ctx, walletService, true)
	if err != nil {
		return nil, err
	}

	nonchangeOutputs := make([]*pb.ConstructTransactionRequest_Output, 1)
	nonchangeOutputs[0] = &pb.ConstructTransactionRequest_Output{
		Amount: int64(ticketCost),
		Destination: &pb.ConstructTransactionRequest_OutputDestination{
			Address: fundingTxAddress.Address(),
		},
	}

	constructTransactionRequest := &pb.ConstructTransactionRequest{
		SourceAccount:            accountNumber,
		RequiredConfirmations:    requiredConfirmations,
		OutputSelectionAlgorithm: pb.ConstructTransactionRequest_UNSPECIFIED,
		NonChangeOutputs:         nonchangeOutputs,
	}

	constructTransactionResponse, err := walletService.ConstructTransaction(ctx, constructTransactionRequest)
	if err != nil {
		return nil, err
	}

	signTransactionRequest := &pb.SignTransactionRequest{
		Passphrase:            []byte("c"),
		SerializedTransaction: constructTransactionResponse.UnsignedTransaction,
	}

	signTransactionResponse, err := walletService.SignTransaction(ctx, signTransactionRequest)
	if err != nil {
		return nil, err
	}

	publishTransactionRequest := &pb.PublishTransactionRequest{
		SignedTransaction: signTransactionResponse.Transaction,
	}

	_, err = walletService.PublishTransaction(ctx, publishTransactionRequest)
	if err != nil {
		return nil, err
	}

	var mtx wire.MsgTx
	err = mtx.Deserialize(bytes.NewReader(signTransactionResponse.Transaction))
	if err != nil {
		return nil, err
	}

	return &mtx, nil

	// decodeRawTransactionRequest := &pb.DecodeRawTransactionRequest{
	// 	SerializedTransaction: signTransactionResponse.Transaction,
	// }

	// decodeMessageService := pb.NewDecodeMessageServiceClient(conn)
	// decodeRawTransactionResponse, err := decodeMessageService.DecodeRawTransaction(ctx, decodeRawTransactionRequest)
	// if err != nil {
	// 	return nil, err
	// }

	// for _, txOut := range decodeRawTransactionResponse.Transaction.Outputs {
	// 	if txOut.Value == int64(ticketCost) {
	// 		fmt.Printf("Found funding output: %s\n", txOut.Addresses[0])

	// 		txHash, err := chainhash.NewHash(decodeRawTransactionResponse.Transaction.TransactionHash)
	// 		if err != nil {
	// 			return nil, err
	// 		}

	// 		txInPrevOutPoint := &wire.OutPoint{Hash: *txHash, Index: txOut.Index}
	// 		return txInPrevOutPoint, nil
	// 	}
	// }

	// return nil, errors.New("funding outpoint not found")
}

func signAndPublishTransaction(ctx context.Context, walletService pb.WalletServiceClient, serializedTx []byte) (hash *chainhash.Hash, err error) {
	signTransactionRequest := &pb.SignTransactionRequest{
		Passphrase:            []byte("c"),
		SerializedTransaction: serializedTx,
	}

	signTransactionResponse, err := walletService.SignTransaction(ctx, signTransactionRequest)
	if err != nil {
		return
	}

	publishTransactionRequest := &pb.PublishTransactionRequest{
		SignedTransaction: signTransactionResponse.Transaction,
	}

	publishTransactionResponse, err := walletService.PublishTransaction(ctx, publishTransactionRequest)
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

func sendPostRequest(marshalledJSON []byte) (*dcrjson.Response, error) {
	url := "https://127.0.0.1:19110"

	bodyReader := bytes.NewReader(marshalledJSON)
	req, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("dcrwallet", "dcrwallet")

	client, err := newHTTPClient()
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

func newHTTPClient() (*http.Client, error) {

	// Configure tls
	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
	}

	pem, err := ioutil.ReadFile(certificateFile)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pem); !ok {
		return nil, fmt.Errorf("invalid certificate file: %v", certificateFile)
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
