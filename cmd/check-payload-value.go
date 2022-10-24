package cmd

import (
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"

	"github.com/flashbots/mev-boost-relay/beaconclient"
	"github.com/metachris/relayscan/common"
	"github.com/metachris/relayscan/database"
	"github.com/onrik/ethrpc"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	slot               uint64
	numPayloads        uint64
	numThreads         uint64
	ethNodeURI         string
	ethNodeBackupURI   string
	checkIncorrectOnly bool
)

func init() {
	rootCmd.AddCommand(checkPayloadValueCmd)
	checkPayloadValueCmd.Flags().Uint64Var(&slot, "slot", 0, "a specific slot")
	checkPayloadValueCmd.Flags().Uint64Var(&numPayloads, "payloads", 1000, "how many payloads")
	checkPayloadValueCmd.Flags().Uint64Var(&numThreads, "threads", 10, "how many threads")
	checkPayloadValueCmd.Flags().StringVar(&ethNodeURI, "eth-node", defaultEthNodeURI, "eth node URI (i.e. Infura)")
	checkPayloadValueCmd.Flags().StringVar(&ethNodeBackupURI, "eth-node-backup", defaultEthBackupNodeURI, "eth node backup URI (i.e. Infura)")
	checkPayloadValueCmd.Flags().BoolVar(&checkIncorrectOnly, "check-incorrect", false, "whether to double-check incorrect values only")
	checkPayloadValueCmd.Flags().StringVar(&beaconNodeURI, "beacon-uri", defaultBeaconURI, "beacon endpoint")
}

var checkPayloadValueCmd = &cobra.Command{
	Use:   "check-payload-value",
	Short: "Check payload value for delivered payloads",
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		log.Infof("Using beacon node: %s", beaconNodeURI)
		bn := beaconclient.NewProdBeaconInstance(log, beaconNodeURI)
		syncStatus, err := bn.SyncStatus()
		if err != nil {
			log.WithError(err).Fatal("couldn't get BN sync status")
		} else if syncStatus.IsSyncing {
			log.Fatal("beacon node is syncing")
		}

		log.Infof("Using eth node: %s", ethNodeURI)
		client := ethrpc.New(ethNodeURI)
		var client2 *ethrpc.EthRPC
		if ethNodeBackupURI != "" {
			log.Infof("Using eth backup node: %s", ethNodeBackupURI)
			client2 = ethrpc.New(ethNodeBackupURI)
		}

		// Connect to Postgres
		dbURL, err := url.Parse(defaultPostgresDSN)
		if err != nil {
			log.WithError(err).Fatalf("couldn't read db URL")
		}
		log.Infof("Connecting to Postgres database at %s%s ...", dbURL.Host, dbURL.Path)
		db, err := database.NewDatabaseService(defaultPostgresDSN)
		if err != nil {
			log.WithError(err).Fatalf("Failed to connect to Postgres database at %s%s", dbURL.Host, dbURL.Path)
		}

		var entries = []database.DataAPIPayloadDeliveredEntry{}
		query := `SELECT id, inserted_at, relay, epoch, slot, parent_hash, block_hash, builder_pubkey, proposer_pubkey, proposer_fee_recipient, gas_limit, gas_used, value_claimed_wei, value_claimed_eth, num_tx, block_number FROM ` + database.TableDataAPIPayloadDelivered
		if checkIncorrectOnly {
			query += ` WHERE value_check_ok=false`
			err = db.DB.Select(&entries, query)
		} else if slot != 0 {
			query += ` WHERE slot=$1`
			err = db.DB.Select(&entries, query, slot)
		} else {
			query += ` WHERE value_check_ok IS NULL ORDER BY slot DESC LIMIT $1`
			err = db.DB.Select(&entries, query, numPayloads)
		}
		if err != nil {
			log.WithError(err).Fatalf("couldn't get entries")
		}

		log.Infof("got %d entries", len(entries))
		if len(entries) == 0 {
			return
		}

		wg := new(sync.WaitGroup)
		entryC := make(chan database.DataAPIPayloadDeliveredEntry)
		if slot != 0 {
			numThreads = 1
		}
		for i := 0; i < int(numThreads); i++ {
			log.Infof("starting worker %d", i+1)
			wg.Add(1)
			go startUpdateWorker(wg, db, bn, client, client2, entryC)
		}

		for _, entry := range entries {
			entryC <- entry
		}
		close(entryC)
		wg.Wait()
	},
}

func _getBalanceDiff(ethClient *ethrpc.EthRPC, address string, blockNumber int) (*big.Int, error) {
	balanceBefore, err := ethClient.EthGetBalance(address, fmt.Sprintf("0x%x", blockNumber-1))
	if err != nil {
		return nil, fmt.Errorf("couldn't get balance for %s @ %d", address, blockNumber-1)
	}
	balanceAfter, err := ethClient.EthGetBalance(address, fmt.Sprintf("0x%x", blockNumber))
	if err != nil {
		return nil, fmt.Errorf("couldn't get balance for %s @ %d", address, blockNumber-1)
	}
	balanceDiff := new(big.Int).Sub(&balanceAfter, &balanceBefore)
	return balanceDiff, nil
}

func startUpdateWorker(wg *sync.WaitGroup, db *database.DatabaseService, bn *beaconclient.ProdBeaconInstance, client, client2 *ethrpc.EthRPC, entryC chan database.DataAPIPayloadDeliveredEntry) {
	defer wg.Done()

	getBalanceDiff := func(address string, blockNumber int) (*big.Int, error) {
		r, err := _getBalanceDiff(client, address, blockNumber)
		if err != nil {
			r, err = _getBalanceDiff(client2, address, blockNumber)
		}
		return r, err
	}

	saveEntry := func(_log *logrus.Entry, entry database.DataAPIPayloadDeliveredEntry) {
		query := `UPDATE ` + database.TableDataAPIPayloadDelivered + ` SET
				block_number=:block_number,
				slot_missed=:slot_missed,
				value_check_ok=:value_check_ok,
				value_check_method=:value_check_method,
				value_delivered_wei=:value_delivered_wei,
				value_delivered_eth=:value_delivered_eth,
				value_delivered_diff_wei=:value_delivered_diff_wei,
				value_delivered_diff_eth=:value_delivered_diff_eth,
				block_coinbase_addr=:block_coinbase_addr,
				block_coinbase_is_proposer=:block_coinbase_is_proposer,
				coinbase_diff_wei=:coinbase_diff_wei,
				coinbase_diff_eth=:coinbase_diff_eth,
				found_onchain=:found_onchain,
				was_uncled=:was_uncled,
				block_hash_onchain=:block_hash_onchain,
				block_hash_onchain_diffs=:block_hash_onchain_diffs
			WHERE slot=:slot`
		_, err := db.DB.NamedExec(query, entry)
		if err != nil {
			_log.WithError(err).Fatalf("failed to save entry")
		}
	}

	var block *ethrpc.Block
	for entry := range entryC {
		_log := log.WithFields(logrus.Fields{
			"slot":        entry.Slot,
			"blockNumber": entry.BlockNumber.Int64,
			"blockHash":   entry.BlockHash,
			"relay":       entry.Relay,
		})
		_log.Infof("checking slot...")
		claimedProposerValue, ok := new(big.Int).SetString(entry.ValueClaimedWei, 10)
		if !ok {
			_log.Fatalf("couldn't convert claimed value to big.Int: %s", entry.ValueClaimedWei)
		}

		// Check if slot was delivered
		_, err := bn.GetHeaderForSlot(entry.Slot)
		entry.SlotWasMissed = database.NewNullBool(false)
		if err != nil {
			if strings.Contains(err.Error(), "Could not find requested block") {
				entry.SlotWasMissed = database.NewNullBool(true)
				_log.Warn("couldn't find block in beacon node, probably missed the proposal!")
			} else {
				_log.WithError(err).Fatalf("couldn't get slot from BN")
			}
		}

		// query block by hash
		block, err = client.EthGetBlockByHash(entry.BlockHash, true)
		if err != nil {
			_log.WithError(err).Fatalf("couldn't get block %s", entry.BlockHash)
		} else if block == nil {
			_log.WithError(err).Warnf("block not found: %s", entry.BlockHash)
			entry.FoundOnChain = database.NewNullBool(false)
			saveEntry(_log, entry)
			continue
		}

		entry.FoundOnChain = database.NewNullBool(true)
		if !entry.BlockNumber.Valid {
			entry.BlockNumber = database.NewNullInt64(int64(block.Number))
		}

		entry.BlockCoinbaseAddress = database.NewNullString(block.Miner)
		coinbaseIsProposer := block.Miner == entry.ProposerFeeRecipient
		entry.BlockCoinbaseIsProposer = database.NewNullBool(coinbaseIsProposer)

		if entry.SlotWasMissed.Bool {
			saveEntry(_log, entry)
			continue
		}

		// query block by number to ensure that's what landed on-chain
		blockByNum, err := client.EthGetBlockByNumber(block.Number, false)
		if err != nil {
			_log.WithError(err).Fatalf("couldn't get block by number %s", block.Number)
		} else if block == nil {
			_log.WithError(err).Warnf("block not found: %s", block.Number)
			continue
		}

		entry.BlockHashOnChain = database.NewNullString(blockByNum.Hash)
		entry.BlockHashOnChainDiffs = database.NewNullBool(blockByNum.Hash != block.Hash)
		if blockByNum.Hash != block.Hash {
			entry.ValueCheckOk = database.NewNullBool(false)
			_log.Warnf("block hash mismatch! payload: %s / by number: %s", entry.BlockHash, blockByNum.Hash)

			// check if it was uncled
			_log.Info("checking for uncling...")
			for i := block.Number + 1; i < block.Number+8; i++ {
				nextBlockByNum, err := client.EthGetBlockByNumber(i, false)
				if err != nil {
					_log.WithError(err).Warnf("couldn't get +block by number %s", i)
				}
				wasUncled := common.StringSliceContains(nextBlockByNum.Uncles, block.Hash)
				_log.Infof("block %d has %d uncles. original block included? %v", i, len(nextBlockByNum.Uncles), wasUncled)
				entry.WasUncled = database.NewNullBool(false)
				if wasUncled {
					_log.Info("-- was uncled!")
					entry.WasUncled = database.NewNullBool(true)
					break
				}
			}
			saveEntry(_log, entry)
			continue
		}

		// Block was found on chain and is same for this blocknumber. Now check the payment!
		checkMethod := "balanceDiffV1"
		proposerBalanceDiffWei, err := getBalanceDiff(entry.ProposerFeeRecipient, block.Number)
		if err != nil {
			_log.WithError(err).Fatalf("couldn't get balance diff")
		}

		proposerValueDiffFromClaim := new(big.Int).Sub(claimedProposerValue, proposerBalanceDiffWei)
		if proposerValueDiffFromClaim.String() != "0" {
			// Value delivered is off. Might be due to a forwarder contract... Checking payment tx...
			checkMethod = "txValue"
			isDeliveredValueIncorrect := true
			if len(block.Transactions) > 0 {
				paymentTx := block.Transactions[len(block.Transactions)-1]
				proposerValueDiffFromClaim = new(big.Int).Sub(claimedProposerValue, &paymentTx.Value)
				if proposerValueDiffFromClaim.String() == "0" {
					_log.Debug("all good, payment is in last tx but was probably forwarded through smart contract")
					isDeliveredValueIncorrect = false
				}
			}

			if isDeliveredValueIncorrect {
				_log.Warnf("Value delivered to %s diffs by %s from claim. delivered: %s - claim: %s - relay: %s - slot: %d / block: %d", entry.ProposerFeeRecipient, proposerValueDiffFromClaim.String(), proposerBalanceDiffWei, entry.ValueClaimedWei, entry.Relay, entry.Slot, block.Number)
			}

			// for i, tx := range block.Transactions {
			// 	if tx.From == entry.ProposerFeeRecipient {
			// 		_log.Infof("- tx %d from feeRecipient with value %s", i, tx.Value.String())
			// 	} else if tx.To == entry.ProposerFeeRecipient && i < len(block.Transactions)-1 {
			// 		_log.Infof("- tx %d to feeRecipient with value %s", i, tx.Value.String())
			// 	}
			// }
		}

		entry.ValueCheckOk = database.NewNullBool(proposerValueDiffFromClaim.String() == "0")
		entry.ValueCheckMethod = database.NewNullString(checkMethod)
		entry.ValueDeliveredWei = database.NewNullString(proposerBalanceDiffWei.String())
		entry.ValueDeliveredEth = database.NewNullString(common.WeiToEth(proposerBalanceDiffWei).String())
		entry.ValueDeliveredDiffWei = database.NewNullString(proposerValueDiffFromClaim.String())
		entry.ValueDeliveredDiffEth = database.NewNullString(common.WeiToEth(proposerValueDiffFromClaim).String())

		if !coinbaseIsProposer {
			// Get builder balance diff
			builderBalanceDiffWei, err := getBalanceDiff(block.Miner, block.Number)
			if err != nil {
				_log.WithError(err).Fatalf("couldn't get balance diff")
			}
			// fmt.Println("builder diff", block.Miner, builderBalanceDiffWei)
			entry.CoinbaseDiffWei = database.NewNullString(builderBalanceDiffWei.String())
			entry.CoinbaseDiffEth = database.NewNullString(common.WeiToEth(builderBalanceDiffWei).String())
		}
		saveEntry(_log, entry)
	}
}
