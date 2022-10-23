package database

import (
	"fmt"
	"math/big"
	"time"
	"unicode/utf8"

	"github.com/flashbots/go-boost-utils/types"
	relaycommon "github.com/flashbots/mev-boost-relay/common"
	"github.com/metachris/relayscan/common"
)

func BidTraceV2JSONToPayloadDeliveredEntry(relay string, entry relaycommon.BidTraceV2JSON) DataAPIPayloadDeliveredEntry {
	wei, ok := new(big.Int).SetString(entry.Value, 10)
	if !ok {
		wei = big.NewInt(0)
	}
	eth := common.WeiToEth(wei)
	ret := DataAPIPayloadDeliveredEntry{
		Relay:                relay,
		Epoch:                entry.Slot / 32,
		Slot:                 entry.Slot,
		ParentHash:           entry.ParentHash,
		BlockHash:            entry.BlockHash,
		BuilderPubkey:        entry.BuilderPubkey,
		ProposerPubkey:       entry.ProposerPubkey,
		ProposerFeeRecipient: entry.ProposerFeeRecipient,
		GasLimit:             entry.GasLimit,
		GasUsed:              entry.GasUsed,
		ValueClaimedWei:      entry.Value,
		ValueClaimedEth:      eth.String(),
	}

	if entry.NumTx > 0 {
		ret.NumTx = NewNullInt64(int64(entry.NumTx))
	}

	if entry.BlockNumber > 0 {
		ret.BlockNumber = NewNullInt64(int64(entry.BlockNumber))
	}
	return ret
}

func BidTraceV2WithTimestampJSONToBuilderBidEntry(relay string, entry relaycommon.BidTraceV2WithTimestampJSON) DataAPIBuilderBidEntry {
	ret := DataAPIBuilderBidEntry{
		Relay:                relay,
		Epoch:                entry.Slot / 32,
		Slot:                 entry.Slot,
		ParentHash:           entry.ParentHash,
		BlockHash:            entry.BlockHash,
		BuilderPubkey:        entry.BuilderPubkey,
		ProposerPubkey:       entry.ProposerPubkey,
		ProposerFeeRecipient: entry.ProposerFeeRecipient,
		GasLimit:             entry.GasLimit,
		GasUsed:              entry.GasUsed,
		Value:                entry.Value,
		Timestamp:            time.Unix(entry.Timestamp, 0).UTC(),
	}

	if entry.NumTx > 0 {
		ret.NumTx = NewNullInt64(int64(entry.NumTx))
	}

	if entry.BlockNumber > 0 {
		ret.BlockNumber = NewNullInt64(int64(entry.BlockNumber))
	}
	return ret
}

func SignedBuilderBidToEntry(relay string, slot uint64, parentHash, proposerPubkey string, timeRequestStart, timeRequestEnd time.Time, bid *types.SignedBuilderBid) SignedBuilderBidEntry {
	extraDataBytes := bid.Message.Header.ExtraData
	for i, b := range extraDataBytes {
		if b < 32 || b > 126 {
			extraDataBytes[i] = 32
		}
	}

	extraData := string(extraDataBytes)
	if !utf8.Valid(bid.Message.Header.ExtraData) {
		extraData = ""
		fmt.Printf("invalid extradata utf8: %s bytes: %s \n", extraData, extraDataBytes)
	}

	return SignedBuilderBidEntry{
		Relay:       relay,
		RequestedAt: timeRequestStart,
		ReceivedAt:  timeRequestEnd,
		LatencyMS:   timeRequestEnd.Sub(timeRequestStart).Milliseconds(),

		Slot:           slot,
		ParentHash:     parentHash,
		ProposerPubkey: proposerPubkey,

		Pubkey:    bid.Message.Pubkey.String(),
		Signature: bid.Signature.String(),

		Value:        bid.Message.Value.String(),
		FeeRecipient: bid.Message.Header.FeeRecipient.String(),
		BlockHash:    bid.Message.Header.BlockHash.String(),
		BlockNumber:  bid.Message.Header.BlockNumber,
		GasLimit:     bid.Message.Header.GasLimit,
		GasUsed:      bid.Message.Header.GasUsed,
		ExtraData:    extraData,
		Epoch:        slot / 32,
	}
}
