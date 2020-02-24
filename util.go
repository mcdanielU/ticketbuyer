package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrjson/v3"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	wallettypes "github.com/decred/dcrwallet/rpc/jsonrpc/types"
	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"github.com/decred/dcrwallet/wallet/v3"
	"github.com/decred/dcrwallet/wallet/v3/txsizes"
)

// NormalizeAddress returns the normalized form of the address, adding a default
// port if necessary.  An error is returned if the address, even without a port,
// is not valid.
func NormalizeAddress(addr string, defaultPort string) (hostport string, err error) {
	// If the first SplitHostPort errors because of a missing port and not
	// for an invalid host, add the port.  If the second SplitHostPort
	// fails, then a port is not missing and the original error should be
	// returned.
	host, port, origErr := net.SplitHostPort(addr)
	if origErr == nil {
		return net.JoinHostPort(host, port), nil
	}
	addr = net.JoinHostPort(addr, defaultPort)
	_, _, err = net.SplitHostPort(addr)
	if err != nil {
		return "", origErr
	}
	return addr, nil
}

func estimateTicketSize(votingAddress dcrutil.Address) int {

	inSizes := []int{txsizes.RedeemP2PKHSigScriptSize}
	outSizes := []int{txsizes.P2PKHPkScriptSize + 1,
		txsizes.TicketCommitmentScriptSize, txsizes.P2PKHPkScriptSize + 1}

	estSize := txsizes.EstimateSerializeSizeFromScriptSizes(inSizes, outSizes, 0)
	fmt.Printf("Estimated Ticket Size: %d\n", estSize)

	return estSize
}

// addressScript returns an output script paying to address.  This func is
// always preferred over direct usage of txscript.PayToAddrScript due to the
// latter failing on unexpected concrete types.
func addressScript(addr dcrutil.Address) (pkScript []byte, version uint16, err error) {
	switch addr := addr.(type) {
	case wallet.V0Scripter:
		return addr.ScriptV0(), 0, nil
	default:
		pkScript, err = txscript.PayToAddrScript(addr)
		return pkScript, 0, err
	}
}

func generateAddress(internal bool, accountNumber uint32, net dcrutil.AddressParams, walletService pb.WalletServiceClient) (address dcrutil.Address, pkScript []byte, err error) {
	ctx := context.Background()
	addressRequest := &pb.NextAddressRequest{
		Account:   accountNumber,
		Kind:      pb.NextAddressRequest_BIP0044_EXTERNAL,
		GapPolicy: pb.NextAddressRequest_GAP_POLICY_WRAP,
	}

	if internal {
		addressRequest.Kind = pb.NextAddressRequest_BIP0044_INTERNAL
	}

	addressResponse, err := walletService.NextAddress(ctx, addressRequest)
	if err != nil {
		return
	}

	address, err = dcrutil.DecodeAddress(addressResponse.Address, net)
	if err != nil {
		return
	}

	pkScript, _, err = addressScript(address)
	return
}

func listUnspentOutputs(cfg *config) ([]wallettypes.ListUnspentResult, error) {
	minConfs := requiredConfirmations
	unspentCmd := wallettypes.NewListUnspentCmd(&minConfs, nil, nil)
	marshalledJSON, err := dcrjson.MarshalCmd(rpcVersion, 1, unspentCmd)
	if err != nil {
		return nil, err
	}

	resp, err := sendPostRequest(cfg.RPCServer, cfg.RPCUser, cfg.RPCPass, marshalledJSON)
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

func signAndPublishTransaction(walletPassphrase string, serializedTx []byte, walletService pb.WalletServiceClient) (hash *chainhash.Hash, err error) {
	ctx := context.Background()
	signTransactionRequest := &pb.SignTransactionRequest{
		Passphrase:            []byte(walletPassphrase),
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
