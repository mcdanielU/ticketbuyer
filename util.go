package main

import (
	"fmt"

	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrwallet/wallet/v3"
	"github.com/decred/dcrwallet/wallet/v3/txsizes"
)

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
