package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/stretchr/testify/suite"

	ethcommon "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	clienttypesv2 "github.com/cosmos/ibc-go/v10/modules/core/02-client/v2/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctesting "github.com/cosmos/ibc-go/v10/testing"

	"github.com/strangelove-ventures/interchaintest/v8/ibc"

	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/alloerc20"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/ics20transfer"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/ics26router"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/sp1ics07tendermint"

	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/chainconfig"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/cosmos"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/e2esuite"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/ethereum"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/relayer"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/testvalues"
	"github.com/srdtrk/solidity-ibc-eureka/e2e/v8/types"
	relayertypes "github.com/srdtrk/solidity-ibc-eureka/e2e/v8/types/relayer"
)

// IbcEurekaTestSuite is a suite of tests that wraps TestSuite
// and can provide additional functionality
type AlloTransfersTestSuite struct {
	e2esuite.TestSuite

	// The private key of a test account
	key *ecdsa.PrivateKey
	// The private key of the faucet account of interchaintest
	deployer *ecdsa.PrivateKey

	contractAddresses ethereum.DeployedContracts
	sp1Ics07Address   ethcommon.Address

	sp1Ics07Contract *sp1ics07tendermint.Contract
	ics26Contract    *ics26router.Contract
	ics20Contract    *ics20transfer.Contract
	alloErc20Contract *alloerc20.Contract

	RelayerClient relayertypes.RelayerServiceClient

	AlloraNetworkRelayerSubmitter ibc.Wallet
	CosmosHubRelayerSubmitter ibc.Wallet

	EthRelayerSubmitter  *ecdsa.PrivateKey
}

// TestWithAlloTransfersTestSuite is the boilerplate code that allows the test suite to be run
func TestWithAlloTransfersTestSuite(t *testing.T) {
	suite.Run(t, new(AlloTransfersTestSuite))
}

// SetupSuite calls the underlying AlloTransfersTestSuite's SetupSuite method
// and deploys the IbcEureka contract
func (s *AlloTransfersTestSuite) SetupSuite(ctx context.Context, proofType types.SupportedProofType, skipAlloInitialMint bool) {
	
	s.TestSuite.SetupSuite(ctx)

	eth, alloraNetworkChain, cosmosHubChain := s.EthChain, s.CosmosChains[0], s.CosmosChains[1]

	var prover string

	s.Require().True(s.Run("Set up environment", func() {
		err := os.Chdir("../..")
		s.Require().NoError(err)

		s.key, err = eth.CreateAndFundUser()
		s.Require().NoError(err)

		s.EthRelayerSubmitter, err = eth.CreateAndFundUser()
		s.Require().NoError(err)

		operatorKey, err := eth.CreateAndFundUser()
		s.Require().NoError(err)

		s.deployer, err = eth.CreateAndFundUser()
		s.Require().NoError(err)

		s.AlloraNetworkRelayerSubmitter = s.CreateAndFundCosmosUser(ctx, alloraNetworkChain)
		s.CosmosHubRelayerSubmitter = s.CreateAndFundCosmosUser(ctx, cosmosHubChain)

		prover = os.Getenv(testvalues.EnvKeySp1Prover)
		switch prover {
		case "", testvalues.EnvValueSp1Prover_Mock:
			s.T().Logf("Using mock prover")
			prover = testvalues.EnvValueSp1Prover_Mock
			os.Setenv(testvalues.EnvKeySp1Prover, testvalues.EnvValueSp1Prover_Mock)
			os.Setenv(testvalues.EnvKeyVerifier, testvalues.EnvValueVerifier_Mock)

			s.Require().Empty(
				os.Getenv(testvalues.EnvKeyGenerateSolidityFixtures),
				"Fixtures are not supported for mock prover",
			)
		case testvalues.EnvValueSp1Prover_Network:
			s.Require().Empty(
				os.Getenv(testvalues.EnvKeyVerifier),
				fmt.Sprintf("%s should not be set when using the network prover in e2e tests.", testvalues.EnvKeyVerifier),
			)
			// make sure that the NETWORK_PRIVATE_KEY is set.
			s.Require().NotEmpty(os.Getenv(testvalues.EnvKeyNetworkPrivateKey))
		default:
			s.Require().Fail("invalid prover type: %s", prover)
		}

		os.Setenv(testvalues.EnvKeyRustLog, testvalues.EnvValueRustLog_Info)
		os.Setenv(testvalues.EnvKeyEthRPC, eth.RPC)
		os.Setenv(testvalues.EnvKeySp1Prover, prover)
		os.Setenv(testvalues.EnvKeyOperatorPrivateKey, hex.EncodeToString(crypto.FromECDSA(operatorKey)))
	}))

	SKIP_ALLO_ERC20_MINT := skipAlloInitialMint;

	s.Require().True(s.Run("Deploy ethereum contracts", func() {
		// Run the deploy script for deploying the solidity contracts
		stdout, err := eth.ForgeScript(s.deployer, testvalues.E2EDeployScriptPath, SKIP_ALLO_ERC20_MINT)
		s.Require().NoError(err)

		// Get the deployed contract addresses from the deploy script output
		s.contractAddresses, err = ethereum.GetEthContractsFromDeployOutput(string(stdout))
		s.Require().NoError(err)
		s.ics26Contract, err = ics26router.NewContract(ethcommon.HexToAddress(s.contractAddresses.Ics26Router), eth.RPCClient)
		s.Require().NoError(err)
		s.ics20Contract, err = ics20transfer.NewContract(ethcommon.HexToAddress(s.contractAddresses.Ics20Transfer), eth.RPCClient)
		s.Require().NoError(err)
		s.alloErc20Contract, err = alloerc20.NewContract(ethcommon.HexToAddress(s.contractAddresses.AlloErc20), eth.RPCClient)
		s.Require().NoError(err)
	}))

	var relayerProcess *os.Process
	s.Require().True(s.Run("Start Relayer", func() {
		beaconAPI := ""
		// The BeaconAPIClient is nil when the testnet is `pow`
		if eth.BeaconAPIClient != nil {
			beaconAPI = eth.BeaconAPIClient.GetBeaconAPIURL()
		}

		sp1Config := relayer.SP1ProverConfig{
			Type:           prover,
			PrivateCluster: os.Getenv(testvalues.EnvKeyNetworkPrivateCluster) == testvalues.EnvValueSp1Prover_PrivateCluster,
		}

		config := relayer.NewConfig(relayer.CreateMultichainModules(
			relayer.MultichainConfigInfo{
				ChainAID:       alloraNetworkChain.Config().ChainID,
				ChainBID:       cosmosHubChain.Config().ChainID,
				EthChainID:     eth.ChainID.String(),
				ChainATmRPC:    alloraNetworkChain.GetHostRPCAddress(),
				ChainBTmRPC:    cosmosHubChain.GetHostRPCAddress(),
				ChainASignerAddress: s.AlloraNetworkRelayerSubmitter.FormattedAddress(),
				ChainBSignerAddress: s.CosmosHubRelayerSubmitter.FormattedAddress(),
				ICS26Address:        s.contractAddresses.Ics26Router,
				EthRPC:              eth.RPC,
				BeaconAPI:           beaconAPI,
				SP1Config:           sp1Config,
				MockWasmClient:      os.Getenv(testvalues.EnvKeyEthTestnetType) == testvalues.EthTestnetTypePoW,
			}),
		)

		err := config.GenerateConfigFile(testvalues.RelayerConfigFilePath)
		s.Require().NoError(err)

		relayerProcess, err = relayer.StartRelayer(testvalues.RelayerConfigFilePath)
		s.Require().NoError(err)

		s.T().Cleanup(func() {
			os.Remove(testvalues.RelayerConfigFilePath)
		})
	}))

	s.T().Cleanup(func() {
		if relayerProcess != nil {
			err := relayerProcess.Kill()
			if err != nil {
				s.T().Logf("Failed to kill the relayer process: %v", err)
			}
		}
	})

	s.Require().True(s.Run("Create Relayer Client", func() {
		s.T().Log("Waiting for relayer to start...")
		time.Sleep(5 * time.Second)

		var err error
		s.RelayerClient, err = relayer.GetGRPCClient(relayer.DefaultRelayerGRPCAddress())
		s.Require().NoError(err)
	}))

	s.Require().True(s.Run("Deploy SP1 ICS07 contract", func() {
		var verfierAddress string
		if prover == testvalues.EnvValueSp1Prover_Mock {
			verfierAddress = s.contractAddresses.VerifierMock
		} else {
			switch proofType {
			case types.ProofTypeGroth16:
				verfierAddress = s.contractAddresses.VerifierGroth16
			case types.ProofTypePlonk:
				verfierAddress = s.contractAddresses.VerifierPlonk
			default:
				s.Require().Fail("invalid proof type: %s", proofType)
			}
		}

		var createClientTxBz []byte
		s.Require().True(s.Run("Retrieve create client tx for Cosmos Hub's client", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: cosmosHubChain.Config().ChainID,
				DstChain: eth.ChainID.String(),
				Parameters: map[string]string{
					testvalues.ParameterKey_Sp1Verifier: verfierAddress,
					testvalues.ParameterKey_ZkAlgorithm: proofType.String(),
				},
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Empty(resp.Address)

			createClientTxBz = resp.Tx
		}))

		s.Require().True(s.Run("Broadcast relay tx for Cosmos Hub's client", func() {
			receipt, err := eth.BroadcastTx(ctx, s.EthRelayerSubmitter, 15_000_000, nil, createClientTxBz)
			s.Require().NoError(err)
			s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status, fmt.Sprintf("Tx failed: %+v", receipt))

			s.sp1Ics07Address = receipt.ContractAddress
			s.sp1Ics07Contract, err = sp1ics07tendermint.NewContract(s.sp1Ics07Address, eth.RPCClient)
			s.Require().NoError(err)
		}))
	}))

	s.Require().True(s.Run("Fund address with ERC20", func() {
		if SKIP_ALLO_ERC20_MINT {
			s.T().Log("Skipped ALLO minting, so no need to fund address with ERC20")
			return
		}

		tx, err := s.alloErc20Contract.Transfer(s.GetTransactOpts(eth.Faucet, eth), crypto.PubkeyToAddress(s.key.PublicKey), testvalues.StartingERC20Balance)
		s.Require().NoError(err)

		_, err = eth.GetTxReciept(ctx, tx.Hash()) // wait for the tx to be mined
		s.Require().NoError(err)
	}))

	s.Require().True(s.Run("Add ethreum light client on Cosmos Hub", func() {
		checksumHex := s.StoreEthereumLightClient(ctx, cosmosHubChain, s.CosmosHubRelayerSubmitter)
		s.Require().NotEmpty(checksumHex)

		var createClientTxBodyBz []byte
		s.Require().True(s.Run("Retrieve create client tx", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: eth.ChainID.String(),
				DstChain: cosmosHubChain.Config().ChainID,
				Parameters: map[string]string{
					testvalues.ParameterKey_ChecksumHex: checksumHex,
				},
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Empty(resp.Address)

			createClientTxBodyBz = resp.Tx
		}))

		s.Require().True(s.Run("Broadcast relay tx", func() {
			resp := s.MustBroadcastSdkTxBody(ctx, cosmosHubChain, s.CosmosHubRelayerSubmitter, 20_000_000, createClientTxBodyBz)
			clientId, err := cosmos.GetEventValue(resp.Events, clienttypes.EventTypeCreateClient, clienttypes.AttributeKeyClientID)
			s.Require().NoError(err)
			s.Require().Equal(testvalues.FirstWasmClientID, clientId)
		}))
	}))

	s.Require().True(s.Run("Add Cosmos Hub client and counterparty on EVM", func() {
		counterpartyInfo := ics26router.IICS02ClientMsgsCounterpartyInfo{
			ClientId:     testvalues.FirstWasmClientID,
			MerklePrefix: [][]byte{[]byte(ibcexported.StoreKey), []byte("")},
		}
		tx, err := s.ics26Contract.AddClient0(s.GetTransactOpts(s.deployer, eth), counterpartyInfo, s.sp1Ics07Address)
		s.Require().NoError(err)

		receipt, err := eth.GetTxReciept(ctx, tx.Hash())
		s.Require().NoError(err)

		event, err := e2esuite.GetEvmEvent(receipt, s.ics26Contract.ParseICS02ClientAdded)
		s.Require().NoError(err)
		s.Require().Equal(testvalues.FirstUniversalClientID, event.ClientId)
		s.Require().Equal(testvalues.FirstWasmClientID, event.CounterpartyInfo.ClientId)
	}))

	s.Require().True(s.Run("Register counterparty on Cosmos Hub", func() {
		merklePathPrefix := [][]byte{[]byte("")}

		_, err := s.BroadcastMessages(ctx, cosmosHubChain, s.CosmosHubRelayerSubmitter, 200_000, &clienttypesv2.MsgRegisterCounterparty{
			ClientId:                 testvalues.FirstWasmClientID,
			CounterpartyClientId:     testvalues.FirstUniversalClientID,
			CounterpartyMerklePrefix: merklePathPrefix,
			Signer:                   s.CosmosHubRelayerSubmitter.FormattedAddress(),
		})
		s.Require().NoError(err)
	}))

	s.Require().True(s.Run("Create Light Client of Cosmos Hub on Allora", func() {
		var createClientTxBodyBz []byte
		s.Require().True(s.Run("Retrieve create client tx", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: cosmosHubChain.Config().ChainID,
				DstChain: alloraNetworkChain.Config().ChainID,
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Empty(resp.Address)

			createClientTxBodyBz = resp.Tx
		}))

		s.Require().True(s.Run("Broadcast relay tx", func() {
			resp := s.MustBroadcastSdkTxBody(ctx, alloraNetworkChain, s.AlloraNetworkRelayerSubmitter, 2_000_000, createClientTxBodyBz)
			clientId, err := cosmos.GetEventValue(resp.Events, clienttypes.EventTypeCreateClient, clienttypes.AttributeKeyClientID)
			s.Require().NoError(err)
			s.Require().Equal(ibctesting.FirstClientID, clientId)
		}))
	}))

	s.Require().True(s.Run("Create Light Client of Allora on Cosmos Hub", func() {
		var createClientTxBodyBz []byte
		s.Require().True(s.Run("Retrieve create client tx", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: alloraNetworkChain.Config().ChainID,
				DstChain: cosmosHubChain.Config().ChainID,
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Empty(resp.Address)

			createClientTxBodyBz = resp.Tx
		}))

		s.Require().True(s.Run("Broadcast relay tx", func() {
			resp := s.MustBroadcastSdkTxBody(ctx, cosmosHubChain, s.CosmosHubRelayerSubmitter, 2_000_000, createClientTxBodyBz)
			clientId, err := cosmos.GetEventValue(resp.Events, clienttypes.EventTypeCreateClient, clienttypes.AttributeKeyClientID)
			s.Require().NoError(err)
			s.Require().Equal(ibctesting.SecondClientID, clientId)
		}))
	}))

	s.Require().True(s.Run("Create Channel and register counterparty on Allora", func() {
		merklePathPrefix := [][]byte{[]byte(ibcexported.StoreKey), []byte("")}

		_, err := s.BroadcastMessages(ctx, alloraNetworkChain, s.AlloraNetworkRelayerSubmitter, 200_000, &clienttypesv2.MsgRegisterCounterparty{
			ClientId:                 ibctesting.FirstClientID,
			CounterpartyClientId:     ibctesting.SecondClientID,
			CounterpartyMerklePrefix: merklePathPrefix,
			Signer:                   s.AlloraNetworkRelayerSubmitter.FormattedAddress(),
		})
		s.Require().NoError(err)
	}))

	s.Require().True(s.Run("Create Channel and register counterparty on Cosmos Hub", func() {
		merklePathPrefix := [][]byte{[]byte(ibcexported.StoreKey), []byte("")}

		_, err := s.BroadcastMessages(ctx, cosmosHubChain, s.CosmosHubRelayerSubmitter, 200_000, &clienttypesv2.MsgRegisterCounterparty{
			ClientId:                 ibctesting.SecondClientID,
			CounterpartyClientId:     ibctesting.FirstClientID,
			CounterpartyMerklePrefix: merklePathPrefix,
			Signer:                   s.CosmosHubRelayerSubmitter.FormattedAddress(),
		})
		s.Require().NoError(err)
	}))
}


func (s *AlloTransfersTestSuite) Test_TransferAlloTokenFromAlloraNetworkToCosmosHubToEthereumAndBack() {
	ctx := context.Background()
	chainconfig.DefaultChainSpecs = append(chainconfig.DefaultChainSpecs, chainconfig.IbcGoChainSpec("ibc-go-cosmoshub", "cosmoshub"))

	s.SetupSuite(ctx, types.ProofTypeGroth16, true)

	eth, alloraNetwork, cosmosHub := s.EthChain, s.CosmosChains[0], s.CosmosChains[1]

	ics20Address := ethcommon.HexToAddress(s.contractAddresses.Ics20Transfer)
	erc20Address := ethcommon.HexToAddress(s.contractAddresses.AlloErc20)
	transferAmount := big.NewInt(testvalues.TransferAmount)
	transferCoin := sdk.NewCoin(alloraNetwork.Config().Denom, sdkmath.NewIntFromBigInt(transferAmount))
	alloraNetworkUser := s.CosmosUsers[0]
	cosmosHubUser := s.CosmosUsers[1]
	ethereumUserAddress := crypto.PubkeyToAddress(s.key.PublicKey)

	// Check initial supply on Ethereum. The initial supply of ALLO on Ethereum is 0.
  s.Require().True(s.Run("Verify initial supply on Ethereum", func() {
    totalSupply, err := s.alloErc20Contract.TotalSupply(nil)
    s.Require().NoError(err)
    s.Require().Zero(totalSupply.Int64())
  }))

	// 
	// Allora Network -> Cosmos Hub
	// 
	var alloraNetworkSendTxHash []byte
	s.Require().True(s.Run("Send from Allora to Cosmos Hub", func() {
		timeout := uint64(time.Now().Add(30 * time.Minute).Unix())

		transferPayload := transfertypes.FungibleTokenPacketData{
			Denom:    transferCoin.Denom,
			Amount:   transferCoin.Amount.String(),
			Sender:   alloraNetworkUser.FormattedAddress(),
			Receiver: cosmosHubUser.FormattedAddress(), // wallet address on simdB
			Memo:     "<bridging instructions - redundant for this test>",
		}

		encodedPayload, err := transfertypes.EncodeABIFungibleTokenPacketData(&transferPayload)
		s.Require().NoError(err)

		payload := channeltypesv2.Payload{
			SourcePort:      transfertypes.PortID,
			DestinationPort: transfertypes.PortID,
			Version:         transfertypes.V1,
			Encoding:        transfertypes.EncodingABI,
			Value:           encodedPayload,
		}

		resp, err := s.BroadcastMessages(ctx, alloraNetwork, alloraNetworkUser, 2_000_000, &channeltypesv2.MsgSendPacket{
			SourceClient:     ibctesting.FirstClientID,
			TimeoutTimestamp: timeout,
			Payloads:         []channeltypesv2.Payload{payload},
			Signer:           alloraNetworkUser.FormattedAddress(),
		})
		s.Require().NoError(err)
		s.Require().NotEmpty(resp.TxHash)

		alloraNetworkSendTxHash, err = hex.DecodeString(resp.TxHash)
		s.Require().NoError(err)
	}))

	denomOnCosmosHub := transfertypes.NewDenom(
		transferCoin.Denom,
		transfertypes.NewHop(transfertypes.PortID, ibctesting.SecondClientID),
	)

	s.Require().True(s.Run("Receive packet on Cosmos Hub", func() {
		var txBodyBz []byte
		s.Require().True(s.Run("Retrieve relay tx to Cosmos Hub", func() {
			resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
				SrcChain:    alloraNetwork.Config().ChainID,
				DstChain:    cosmosHub.Config().ChainID,
				SourceTxIds: [][]byte{alloraNetworkSendTxHash},
				SrcClientId: ibctesting.FirstClientID,
				DstClientId: ibctesting.SecondClientID,
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Empty(resp.Address)

			txBodyBz = resp.Tx
		}))

		s.Require().True(s.Run("Broadcast relay tx on Cosmos Hub", func() {
			_ = s.MustBroadcastSdkTxBody(ctx, cosmosHub, s.CosmosHubRelayerSubmitter, 2_000_000, txBodyBz)
		}))

		s.Require().True(s.Run("Verify balances on Cosmos Hub", func() {
			resp, err := e2esuite.GRPCQuery[banktypes.QueryBalanceResponse](ctx, cosmosHub, &banktypes.QueryBalanceRequest{
				Address: cosmosHubUser.FormattedAddress(),
				Denom:   denomOnCosmosHub.IBCDenom(),
			})
			s.Require().NoError(err)
			s.Require().NotNil(resp.Balance)
			s.Require().Equal(transferAmount, resp.Balance.Amount.BigInt())
			s.Require().Equal(denomOnCosmosHub.IBCDenom(), resp.Balance.Denom)
		}))
	}))

	// 
	// Cosmos Hub -> Ethereum
	// 
	var cosmosHubTransferTxHash []byte
	s.Require().True(s.Run("Transfer tokens from Cosmos Hub to Ethereum", func() {
		timeout := uint64(time.Now().Add(30 * time.Minute).Unix())
		transferCoin := sdk.NewCoin(denomOnCosmosHub.IBCDenom(), sdkmath.NewIntFromBigInt(transferAmount))
		transferPayload := transfertypes.FungibleTokenPacketData{
			Denom:    denomOnCosmosHub.Path(),
			Amount:   transferCoin.Amount.String(),
			Sender:   cosmosHubUser.FormattedAddress(),
			Receiver: strings.ToLower(ethereumUserAddress.Hex()),
			Memo:     "",
		}
		encodedPayload, err := transfertypes.EncodeABIFungibleTokenPacketData(&transferPayload)
		s.Require().NoError(err)

		payload := channeltypesv2.Payload{
			SourcePort:      transfertypes.PortID,
			DestinationPort: transfertypes.PortID,
			Version:         transfertypes.V1,
			Encoding:        transfertypes.EncodingABI,
			Value:           encodedPayload,
		}
		msgSendPacket := &channeltypesv2.MsgSendPacket{
			SourceClient:     testvalues.FirstWasmClientID,
			TimeoutTimestamp: timeout,
			Payloads:         []channeltypesv2.Payload{payload},
			Signer:           cosmosHubUser.FormattedAddress(),
		}

		resp, err := s.BroadcastMessages(ctx, cosmosHub, cosmosHubUser, 2_000_000, msgSendPacket)
		s.Require().NoError(err)
		s.Require().NotEmpty(resp.TxHash)

		cosmosHubTransferTxHash, err = hex.DecodeString(resp.TxHash)
		s.Require().NoError(err)
	}))

	s.Require().True(s.Run("Receive packet on Ethereum", func() {
		ics26Address := ethcommon.HexToAddress(s.contractAddresses.Ics26Router)

		var relayTxBodyBz []byte
		s.Require().True(s.Run("Retrieve relay tx", func() {
			resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
				SrcChain:    cosmosHub.Config().ChainID,
				DstChain:    eth.ChainID.String(),
				SourceTxIds: [][]byte{cosmosHubTransferTxHash},
				SrcClientId: testvalues.FirstWasmClientID,
				DstClientId: testvalues.FirstUniversalClientID,
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Equal(ics26Address.String(), resp.Address)

			relayTxBodyBz = resp.Tx
		}))

		s.Require().True(s.Run("Submit relay tx", func() {
			receipt, err := eth.BroadcastTx(ctx, s.EthRelayerSubmitter, 5_000_000, &ics26Address, relayTxBodyBz)
			s.Require().NoError(err)
			s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status, fmt.Sprintf("Tx failed: %+v", receipt))
		}))

		s.True(s.Run("Verify balances on Ethereum", func() {
			alloUserBalance, err := s.alloErc20Contract.BalanceOf(nil, ethereumUserAddress)
			s.Require().NoError(err)
			s.Require().Equal(transferAmount, alloUserBalance)
		}))
	}))

	// 
	// Ethreum -> Cosmos Hub
	// 
	var ethReturnSendTxHash []byte
	s.Require().True(s.Run("Transfer tokens from Ethereum to ", func() {
		s.Require().True(s.Run("Approve the ICS20Transfer.sol contract to spend the erc20 tokens", func() {
			tx, err := s.alloErc20Contract.Approve(s.GetTransactOpts(s.key, eth), ics20Address, transferAmount)
			s.Require().NoError(err)

			receipt, err := eth.GetTxReciept(ctx, tx.Hash())
			s.Require().NoError(err)
			s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status)

			allowance, err := s.alloErc20Contract.Allowance(nil, ethereumUserAddress, ics20Address)
			s.Require().NoError(err)
			s.Require().Equal(transferAmount, allowance)
		}))

		s.Require().True(s.Run("Send packet on Ethereum", func() {
			timeout := uint64(time.Now().Add(30 * time.Minute).Unix())
			msgSendPacket := ics20transfer.IICS20TransferMsgsSendTransferMsg{
				Denom:            erc20Address,
				Amount:           transferAmount,
				Receiver:         cosmosHubUser.FormattedAddress(),
				TimeoutTimestamp: timeout,
				SourceClient:     testvalues.FirstUniversalClientID,
				DestPort:         transfertypes.PortID,
				Memo:             "testmemo",
			}

			tx, err := s.ics20Contract.SendTransfer(s.GetTransactOpts(s.key, eth), msgSendPacket)
			s.Require().NoError(err)

			receipt, err := eth.GetTxReciept(ctx, tx.Hash())
			s.Require().NoError(err)
			s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status)

			ethReturnSendTxHash = tx.Hash().Bytes()
		}))

		s.Require().True(s.Run("Receive packet on Cosmos Hub", func() {
			var returnRelayTxBodyBz []byte
			s.Require().True(s.Run("Retrieve relay tx", func() {
				resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
					SrcChain:    eth.ChainID.String(),
					DstChain:    cosmosHub.Config().ChainID,
					SourceTxIds: [][]byte{ethReturnSendTxHash},
					SrcClientId: testvalues.FirstUniversalClientID,
					DstClientId: testvalues.FirstWasmClientID,
				})
				s.Require().NoError(err)
				s.Require().NotEmpty(resp.Tx)
				s.Require().Empty(resp.Address)

				returnRelayTxBodyBz = resp.Tx
			}))

			s.Require().True(s.Run("Broadcast relay tx on Cosmos Hub", func() {
				_ = s.MustBroadcastSdkTxBody(ctx, cosmosHub, s.CosmosHubRelayerSubmitter, 2_000_000, returnRelayTxBodyBz)
			}))

			s.Require().True(s.Run("Verify balances on Cosmos Hub", func() {
				resp, err := e2esuite.GRPCQuery[banktypes.QueryBalanceResponse](ctx, cosmosHub, &banktypes.QueryBalanceRequest{
					Address: cosmosHubUser.FormattedAddress(),
					Denom:   denomOnCosmosHub.IBCDenom(),
				})
				s.Require().NoError(err)
				s.Require().NotNil(resp.Balance)
				s.Require().Equal(transferAmount, resp.Balance.Amount.BigInt())
				s.Require().Equal(denomOnCosmosHub.IBCDenom(), resp.Balance.Denom)
			}))
		}))

		// 
		// Cosmos Hub -> Allora Network
		// 
		s.Require().True(s.Run("Transfer tokens from Cosmos Hub to Allora", func() {
			s.Require().True(s.Run("Send packet on Cosmos Hub", func() {
				timeout := uint64(time.Now().Add(30 * time.Minute).Unix())
				transferPayload := transfertypes.FungibleTokenPacketData{
					Denom:    denomOnCosmosHub.Path(),
					Amount:   transferAmount.String(),
					Sender:   cosmosHubUser.FormattedAddress(),
					Receiver: alloraNetworkUser.FormattedAddress(),
					Memo:     "",
				}
	
				encodedPayload, err := transfertypes.EncodeABIFungibleTokenPacketData(&transferPayload)
				s.Require().NoError(err)
	
				payload := channeltypesv2.Payload{
					SourcePort:      transfertypes.PortID,
					DestinationPort: transfertypes.PortID,
					Version:         transfertypes.V1,
					Encoding:        transfertypes.EncodingABI,
					Value:           encodedPayload,
				}
	
				msgSendPacket := &channeltypesv2.MsgSendPacket{
					SourceClient:     ibctesting.SecondClientID,
					TimeoutTimestamp: timeout,
					Payloads:         []channeltypesv2.Payload{payload},
					Signer:           cosmosHubUser.FormattedAddress(),
				}
	
				resp, err := s.BroadcastMessages(ctx, cosmosHub, cosmosHubUser, 2_000_000, msgSendPacket)
				s.Require().NoError(err)
				s.Require().NotEmpty(resp.TxHash)
	
				cosmosHubTransferTxHash, err = hex.DecodeString(resp.TxHash)
				s.Require().NoError(err)
			}))
	
			s.Require().True(s.Run("Receive packet on Allora", func() {
				var returnRelayTxBodyBz []byte
				s.Require().True(s.Run("Retrieve relay tx", func() {
					resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
						SrcChain:    cosmosHub.Config().ChainID,
						DstChain:    alloraNetwork.Config().ChainID,
						SourceTxIds: [][]byte{cosmosHubTransferTxHash},
						SrcClientId: ibctesting.SecondClientID,
						DstClientId: ibctesting.FirstClientID,
					})
					s.Require().NoError(err)
					s.Require().NotEmpty(resp.Tx)
					s.Require().Empty(resp.Address)
	
					returnRelayTxBodyBz = resp.Tx
				}))
	
				s.Require().True(s.Run("Broadcast relay tx on Allora", func() {
					_ = s.MustBroadcastSdkTxBody(ctx, alloraNetwork, s.AlloraNetworkRelayerSubmitter, 2_000_000, returnRelayTxBodyBz)
				}))
	
				s.Require().True(s.Run("Verify balances on Allora", func() {
					resp, err := e2esuite.GRPCQuery[banktypes.QueryBalanceResponse](ctx, alloraNetwork, &banktypes.QueryBalanceRequest{
						Address: alloraNetworkUser.FormattedAddress(),
						Denom:   transferCoin.Denom,
					})
					s.Require().NoError(err)
					s.Require().NotNil(resp.Balance)
					s.Require().Equal(testvalues.InitialBalance, resp.Balance.Amount.Int64())
				}))
			}))
		}))
	}))
}
