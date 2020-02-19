package main

import (
	"bytes"
	"context"
	"crypto/subtle"

	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrwallet/errors/v2"
)

type missingGenError struct{}

var errMissingGen missingGenError

func (missingGenError) Error() string   { return "coinjoin is missing gen output" }
func (missingGenError) MissingMessage() {}

type CsppJoin struct {
	tx            *wire.MsgTx
	txInputs      map[wire.OutPoint]int
	myPrevScripts [][]byte
	myIns         []*wire.TxIn
	change        *wire.TxOut
	genScripts    [][]byte
	genIndex      []int
	amount        int64
	tb            *TicketBuyer

	ctx context.Context
}

func (tb *TicketBuyer) newCsppJoin(ctx context.Context, change *wire.TxOut, amount dcrutil.Amount) *CsppJoin {
	cj := &CsppJoin{
		tx:     &wire.MsgTx{Version: 1},
		change: change,
		amount: int64(amount),
		tb:     tb,
		ctx:    ctx,
	}
	if change != nil {
		cj.tx.TxOut = append(cj.tx.TxOut, change)
	}
	return cj
}

func (c *CsppJoin) addTxIn(prevScript []byte, in *wire.TxIn) {
	c.tx.TxIn = append(c.tx.TxIn, in)
	c.myPrevScripts = append(c.myPrevScripts, prevScript)
	c.myIns = append(c.myIns, in)
}

func (c *CsppJoin) Gen() ([][]byte, error) {
	const op errors.Op = "cspp.Gen"

	mixAddr, pkScript, err := c.tb.generateAddress(true)
	if err != nil {
		return nil, err
	}

	gen := make([][]byte, 1)
	c.genScripts = make([][]byte, 1)

	c.genScripts[0] = pkScript
	gen[0] = mixAddr.Hash160()[:]

	

	return gen, nil
}

func (c *CsppJoin) Confirm() error {
	const op errors.Op = "cspp.Confirm"

	for outx, in := range c.myIns {
		outScript := c.myPrevScripts[outx]
		index, ok := c.txInputs[in.PreviousOutPoint]
		if !ok {
			return errors.E("coinjoin is missing inputs")
		}
		in = c.tx.TxIn[index]

		const scriptVersion = 0
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(scriptVersion, outScript, c.tb.netParams)
		if err != nil {
			return err
		}
		if len(addrs) != 1 {
			continue
		}
		apkh, ok := addrs[0].(*dcrutil.AddressPubKeyHash)
		if !ok {
			return errors.E(errors.Bug, "previous output is not P2PKH")
		}
		privKey, err := c.tb.privateKeyForAddress(apkh)
		if err != nil {
			return err
		}

		sigscript, err := txscript.SignatureScript(c.tx, index, outScript,
			txscript.SigHashAll, privKey, true)
		if err != nil {
			return errors.E(errors.Op("txscript.SignatureScript"), err)
		}
		in.SignatureScript = sigscript
	}
	return nil
}

func (c *CsppJoin) mixOutputIndexes() []int {
	return c.genIndex
}

func (c *CsppJoin) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	buf.Grow(c.tx.SerializeSize())
	err := c.tx.Serialize(buf)
	return buf.Bytes(), err
}

func (c *CsppJoin) UnmarshalBinary(b []byte) error {
	tx := new(wire.MsgTx)
	err := tx.Deserialize(bytes.NewReader(b))
	if err != nil {
		return err
	}

	// Ensure all unmixed inputs, unmixed outputs, and mixed outputs exist.
	// Mixed outputs must be searched in constant time to avoid sidechannel leakage.
	txInputs := make(map[wire.OutPoint]int, len(tx.TxIn))
	for i, in := range tx.TxIn {
		txInputs[in.PreviousOutPoint] = i
	}
	var n int
	for _, in := range c.myIns {
		if index, ok := txInputs[in.PreviousOutPoint]; ok {
			other := tx.TxIn[index]
			if in.Sequence != other.Sequence || in.ValueIn != other.ValueIn {
				break
			}
			n++
		}
	}
	if n != len(c.myIns) {
		return errors.E("coinjoin is missing inputs")
	}
	if c.change != nil {
		var hasChange bool
		for _, out := range tx.TxOut {
			if out.Value != c.change.Value {
				continue
			}
			if out.Version != c.change.Version {
				continue
			}
			if !bytes.Equal(out.PkScript, c.change.PkScript) {
				continue
			}
			hasChange = true
			break
		}
		if !hasChange {
			return errors.E("coinjoin is missing change")
		}
	}
	indexes, err := constantTimeOutputSearch(tx, c.amount, 0, c.genScripts)
	if err != nil {
		return err
	}

	c.tx = tx
	c.txInputs = txInputs
	c.genIndex = indexes
	return nil
}

// constantTimeOutputSearch searches for the output indexes of mixed outputs to
// verify inclusion in a coinjoin.  It is constant time such that, for each
// searched script, all outputs with equal value, script versions, and script
// lengths matching the searched output are checked in constant time.
func constantTimeOutputSearch(tx *wire.MsgTx, value int64, scriptVer uint16, scripts [][]byte) ([]int, error) {
	var scan []int
	for i, out := range tx.TxOut {
		if out.Value != value {
			continue
		}
		if out.Version != scriptVer {
			continue
		}
		if len(out.PkScript) != len(scripts[0]) {
			continue
		}
		scan = append(scan, i)
	}
	indexes := make([]int, 0, len(scan))
	var missing int
	for _, s := range scripts {
		idx := -1
		for _, i := range scan {
			eq := subtle.ConstantTimeCompare(tx.TxOut[i].PkScript, s)
			idx = subtle.ConstantTimeSelect(eq, i, idx)
		}
		indexes = append(indexes, idx)
		eq := subtle.ConstantTimeEq(int32(idx), -1)
		missing = subtle.ConstantTimeSelect(eq, 1, missing)
	}
	if missing == 1 {
		return nil, errMissingGen
	}
	return indexes, nil
}
