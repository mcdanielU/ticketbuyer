package main

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
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

type RegularTransaction struct {
	cfg           *config
	outputScript  []byte
	changeScript  []byte
	outputAmount  dcrutil.Amount
	utxos         []wallettypes.ListUnspentResult
	walletService pb.WalletServiceClient
}

func NewRegularTransaction(cfg *config, outputScript, changeScript []byte, outputAmount dcrutil.Amount, utxos []wallettypes.ListUnspentResult, walletService pb.WalletServiceClient) *RegularTransaction {
	return &RegularTransaction{
		cfg:           cfg,
		outputScript:  outputScript,
		changeScript:  changeScript,
		outputAmount:  outputAmount,
		utxos:         utxos,
		walletService: walletService,
	}
}

func (rt *RegularTransaction) broadcastTransaction() (*wire.MsgTx, error) {

	mtx := wire.NewMsgTx()

	txOut := wire.NewTxOut(int64(rt.outputAmount), rt.outputScript)
	mtx.AddTxOut(txOut)

	changeScriptSize := txsizes.P2PKHPkScriptSize

	// init'd with a single script for inital tx fee estimation
	scriptSizes := []int{txsizes.RedeemP2PKHSigScriptSize}

	maxSignedSize := txsizes.EstimateSerializeSize(scriptSizes, mtx.TxOut, changeScriptSize)
	targetFee := txrules.FeeForSerializeSize(txRelayFeeDCR, maxSignedSize)

	for {
		inputDetail, err := rt.selectInputsForAmount(rt.outputAmount + targetFee)
		if err != nil {
			return nil, err
		}

		if inputDetail.Amount < rt.outputAmount+targetFee {
			return nil, errors.E(errors.InsufficientBalance)
		}

		scriptSizes := make([]int, 0, len(inputDetail.RedeemScriptSizes))
		scriptSizes = append(scriptSizes, inputDetail.RedeemScriptSizes...)

		maxSignedSize = txsizes.EstimateSerializeSize(scriptSizes, mtx.TxOut, changeScriptSize)
		maxRequiredFee := txrules.FeeForSerializeSize(txRelayFeeDCR, maxSignedSize)
		remainingAmount := inputDetail.Amount - rt.outputAmount
		if remainingAmount < maxRequiredFee {
			targetFee = maxRequiredFee
			continue
		}

		mtx.TxIn = inputDetail.Inputs
		mtx.SerType = wire.TxSerializeFull
		mtx.Version = generatedTxVersion

		changeAmount := inputDetail.Amount - rt.outputAmount - maxRequiredFee
		if changeAmount != 0 && !txrules.IsDustAmount(changeAmount, changeScriptSize, txRelayFeeDCR) {

			if len(rt.changeScript) > txscript.MaxScriptElementSize {
				return nil, errors.E(errors.Invalid, "script size exceed maximum bytes "+
					"pushable to the stack")
			}

			change := &wire.TxOut{
				Value:    int64(changeAmount),
				PkScript: rt.changeScript,
			}

			mtx.AddTxOut(change)
		}

		serializedTx, err := mtx.Bytes()
		if err != nil {
			return nil, err
		}

		_, err = signAndPublishTransaction(rt.cfg.WalletPassphrase, serializedTx, rt.walletService)
		if err != nil {
			return nil, err
		}

		return mtx, nil
	}

}

func (rt *RegularTransaction) selectInputsForAmount(targetAmount dcrutil.Amount) (*txauthor.InputDetail, error) {

	var (
		currentTotal      dcrutil.Amount
		currentInputs     []*wire.TxIn
		currentScripts    [][]byte
		redeemScriptSizes []int
	)

	unspentOutputs := rt.utxos

	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(unspentOutputs), func(i, j int) {
		unspentOutputs[i], unspentOutputs[j] = unspentOutputs[j], unspentOutputs[i]
	})

	for _, unspentOutput := range unspentOutputs {
		if unspentOutput.Spendable && unspentOutput.Account == rt.cfg.SourceAccountName {
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
