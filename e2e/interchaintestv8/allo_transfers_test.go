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

	"github.com/strangelove-ventures/interchaintest/v8/ibc"

	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/alloerc20"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/ics20transfer"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/ics26router"
	"github.com/cosmos/solidity-ibc-eureka/packages/go-abigen/sp1ics07tendermint"

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

	SimdRelayerSubmitter ibc.Wallet
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

	eth, simd := s.EthChain, s.CosmosChains[0]

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

		s.SimdRelayerSubmitter = s.CreateAndFundCosmosUser(ctx, simd)

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

		if os.Getenv(testvalues.EnvKeyRustLog) == "" {
			os.Setenv(testvalues.EnvKeyRustLog, testvalues.EnvValueRustLog_Info)
		}
		os.Setenv(testvalues.EnvKeyEthRPC, eth.RPC)
		os.Setenv(testvalues.EnvKeyTendermintRPC, simd.GetHostRPCAddress())
		os.Setenv(testvalues.EnvKeySp1Prover, prover)
		os.Setenv(testvalues.EnvKeyOperatorPrivateKey, hex.EncodeToString(crypto.FromECDSA(operatorKey)))
	}))

	SKIP_ALLO_ERC20_MINT := skipAlloInitialMint;

	s.Require().True(s.Run("Deploy IBC contracts", func() {
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

		config := relayer.NewConfig(relayer.CreateEthCosmosModules(
			relayer.EthCosmosConfigInfo{
				EthChainID:     eth.ChainID.String(),
				CosmosChainID:  simd.Config().ChainID,
				TmRPC:          simd.GetHostRPCAddress(),
				ICS26Address:   s.contractAddresses.Ics26Router,
				EthRPC:         eth.RPC,
				BeaconAPI:      beaconAPI,
				SP1Config:      sp1Config,
				SignerAddress:  s.SimdRelayerSubmitter.FormattedAddress(),
				MockWasmClient: os.Getenv(testvalues.EnvKeyEthTestnetType) == testvalues.EthTestnetTypePoW,
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
		s.Require().True(s.Run("Retrieve create client tx", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: simd.Config().ChainID,
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

		s.Require().True(s.Run("Broadcast relay tx", func() {
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

	s.Require().True(s.Run("Create ethereum light client on Cosmos chain", func() {
		checksumHex := s.StoreEthereumLightClient(ctx, simd, s.SimdRelayerSubmitter)
		s.Require().NotEmpty(checksumHex)

		var createClientTxBodyBz []byte
		s.Require().True(s.Run("Retrieve create client tx", func() {
			resp, err := s.RelayerClient.CreateClient(context.Background(), &relayertypes.CreateClientRequest{
				SrcChain: eth.ChainID.String(),
				DstChain: simd.Config().ChainID,
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
			resp := s.MustBroadcastSdkTxBody(ctx, simd, s.SimdRelayerSubmitter, 20_000_000, createClientTxBodyBz)
			clientId, err := cosmos.GetEventValue(resp.Events, clienttypes.EventTypeCreateClient, clienttypes.AttributeKeyClientID)
			s.Require().NoError(err)
			s.Require().Equal(testvalues.FirstWasmClientID, clientId)
		}))
	}))

  s.Require().True(s.Run("Add client and counterparty on EVM", func() {
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

	s.Require().True(s.Run("Register counterparty on Cosmos chain", func() {
		merklePathPrefix := [][]byte{[]byte("")}

		_, err := s.BroadcastMessages(ctx, simd, s.SimdRelayerSubmitter, 200_000, &clienttypesv2.MsgRegisterCounterparty{
			ClientId:                 testvalues.FirstWasmClientID,
			CounterpartyMerklePrefix: merklePathPrefix,
			CounterpartyClientId:     testvalues.FirstUniversalClientID,
			Signer:                   s.SimdRelayerSubmitter.FormattedAddress(),
		})
		s.Require().NoError(err)
	}))
}

// DeployTest tests the deployment of the AlloTransfers contracts
func (s *AlloTransfersTestSuite) DeployTest(ctx context.Context, proofType types.SupportedProofType, skipAlloInitialMint bool) {
	s.SetupSuite(ctx, proofType, skipAlloInitialMint)

	eth, simd := s.EthChain, s.CosmosChains[0]

	s.Require().True(s.Run("Verify SP1 Client", func() {
		clientState, err := s.sp1Ics07Contract.ClientState(nil)
		s.Require().NoError(err)

		stakingParams, err := simd.StakingQueryParams(ctx)
		s.Require().NoError(err)

		s.Require().Equal(simd.Config().ChainID, clientState.ChainId)
		s.Require().Equal(uint8(testvalues.DefaultTrustLevel.Numerator), clientState.TrustLevel.Numerator)
		s.Require().Equal(uint8(testvalues.DefaultTrustLevel.Denominator), clientState.TrustLevel.Denominator)
		s.Require().Equal(uint32(testvalues.DefaultTrustPeriod), clientState.TrustingPeriod)
		s.Require().Equal(uint32(stakingParams.UnbondingTime.Seconds()), clientState.UnbondingPeriod)
		s.Require().False(clientState.IsFrozen)
		s.Require().Equal(uint64(1), clientState.LatestHeight.RevisionNumber)
		s.Require().Greater(clientState.LatestHeight.RevisionHeight, uint64(0))
	}))

	s.Require().True(s.Run("Verify ICS02 Client", func() {
		clientAddress, err := s.ics26Contract.GetClient(nil, testvalues.FirstUniversalClientID)
		s.Require().NoError(err)
		s.Require().Equal(s.sp1Ics07Address, clientAddress)

		counterpartyInfo, err := s.ics26Contract.GetCounterparty(nil, testvalues.FirstUniversalClientID)
		s.Require().NoError(err)
		s.Require().Equal(testvalues.FirstWasmClientID, counterpartyInfo.ClientId)
	}))

	s.Require().True(s.Run("Verify ICS26 Router", func() {
		hasRole, err := s.ics26Contract.HasRole(nil, testvalues.PortCustomizerRole, crypto.PubkeyToAddress(s.deployer.PublicKey))
		s.Require().NoError(err)
		s.Require().True(hasRole)

		transferAddress, err := s.ics26Contract.GetIBCApp(nil, transfertypes.PortID)
		s.Require().NoError(err)
		s.Require().Equal(s.contractAddresses.Ics20Transfer, strings.ToLower(transferAddress.Hex()))
	}))

	s.Require().True(s.Run("Verify ethereum light client", func() {
		_, err := e2esuite.GRPCQuery[clienttypes.QueryClientStateResponse](ctx, simd, &clienttypes.QueryClientStateRequest{
			ClientId: testvalues.FirstWasmClientID,
		})
		s.Require().NoError(err)

		counterpartyInfoResp, err := e2esuite.GRPCQuery[clienttypesv2.QueryCounterpartyInfoResponse](ctx, simd, &clienttypesv2.QueryCounterpartyInfoRequest{
			ClientId: testvalues.FirstWasmClientID,
		})
		s.Require().NoError(err)
		s.Require().Equal(testvalues.FirstUniversalClientID, counterpartyInfoResp.CounterpartyInfo.ClientId)
	}))

	s.Require().True(s.Run("Verify Cosmos to Eth Relayer Info", func() {
		info, err := s.RelayerClient.Info(context.Background(), &relayertypes.InfoRequest{
			SrcChain: simd.Config().ChainID,
			DstChain: eth.ChainID.String(),
		})
		s.Require().NoError(err)
		s.Require().NotNil(info)
		s.Require().Equal(simd.Config().ChainID, info.SourceChain.ChainId)
		s.Require().Equal(eth.ChainID.String(), info.TargetChain.ChainId)
	}))

	s.Require().True(s.Run("Verify Eth to Cosmos Relayer Info", func() {
		info, err := s.RelayerClient.Info(context.Background(), &relayertypes.InfoRequest{
			SrcChain: eth.ChainID.String(),
			DstChain: simd.Config().ChainID,
		})
		s.Require().NoError(err)
		s.Require().NotNil(info)
		s.Require().Equal(eth.ChainID.String(), info.SourceChain.ChainId)
		s.Require().Equal(simd.Config().ChainID, info.TargetChain.ChainId)
	}))

	s.True(s.Run("Verify balances on Ethereum", func() {
		if skipAlloInitialMint {
			s.T().Log("Skipped ALLO minting")

      // User balance on Ethereum
      alloTotalSupply, err := s.alloErc20Contract.TotalSupply(nil)
      s.Require().NoError(err)
      s.Require().Equal(0, alloTotalSupply)

      // Get the escrow address
      escrowAddress, err := s.ics20Contract.GetEscrow(nil, testvalues.FirstUniversalClientID)
      s.Require().NoError(err)

      // ICS20 contract balance on Ethereum
      escrowBalance, err := s.alloErc20Contract.BalanceOf(nil, escrowAddress)
      s.Require().NoError(err)
      s.Require().Equal(0, escrowBalance)

			return
		} else {
      s.T().Log("NOT skipped ALLO minting")

      // User balance on Ethereum
      alloTotalSupply, err := s.alloErc20Contract.TotalSupply(nil)
      s.Require().NoError(err)
      s.Require().Equal(testvalues.MaxUint256.ToBig(), alloTotalSupply)

      // Get the escrow address
      escrowAddress, err := s.ics20Contract.GetEscrow(nil, testvalues.FirstUniversalClientID)
      s.Require().NoError(err)

      // ICS20 contract balance on Ethereum
      escrowBalance, err := s.alloErc20Contract.BalanceOf(nil, escrowAddress)
      s.Require().NoError(err)
      s.Require().Equal(0, escrowBalance)
    }
	}))
}

func (s *AlloTransfersTestSuite) TestICS20TransferAlloTokenfromCosmosToEthereumAndBack() {
	ctx := context.Background()
	s.ICS20TransferAlloTokenfromCosmosToEthereumAndBackTest(ctx, types.ProofTypeGroth16)
}


func (s *AlloTransfersTestSuite) ICS20TransferAlloTokenfromCosmosToEthereumAndBackTest(ctx context.Context, proofType types.SupportedProofType) {
	s.SetupSuite(ctx, proofType, true)

  _, simd := s.EthChain, s.CosmosChains[0]
  eth, simd := s.EthChain, s.CosmosChains[0]
	ics26Address := ethcommon.HexToAddress(s.contractAddresses.Ics26Router)
	ics20Address := ethcommon.HexToAddress(s.contractAddresses.Ics20Transfer)
	erc20Address := ethcommon.HexToAddress(s.contractAddresses.AlloErc20)
	totalTransferAmount := big.NewInt(testvalues.InitialBalance)

	ethereumUserAddress := crypto.PubkeyToAddress(s.key.PublicKey)
	cosmosUserWallet := s.CosmosUsers[0]
	cosmosUserAddress := cosmosUserWallet.FormattedAddress()

  var (
    returnSendTxHash []byte
    ibcCoin sdk.Coin
    err error
  )

  // Check initial supply on Ethereum. The initial supply of ALLO on Ethereum is 0.
  s.Require().True(s.Run("Verify initial supply on Ethereum", func() {
    totalSupply, err := s.alloErc20Contract.TotalSupply(nil)
    s.Require().NoError(err)
    s.Require().Zero(totalSupply.Int64())
  }))

  s.Require().True(s.Run("Send transfer on the Cosmos chain", func() {
    timeout := uint64(time.Now().Add(30 * time.Minute).Unix())
    ibcCoin = sdk.NewCoin(simd.Config().Denom, sdkmath.NewIntFromBigInt(totalTransferAmount))

    transferPayload := transfertypes.FungibleTokenPacketData{
      Denom:    ibcCoin.Denom,
      Amount:   ibcCoin.Amount.String(),
      Sender:   cosmosUserWallet.FormattedAddress(),
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

    msgSendPacket := channeltypesv2.MsgSendPacket{
      SourceClient:     testvalues.FirstWasmClientID,
      TimeoutTimestamp: timeout,
      Payloads: []channeltypesv2.Payload{
        payload,
      },
      Signer: cosmosUserAddress,
    }

    resp, err := s.BroadcastMessages(ctx, simd, cosmosUserWallet, 20_000_000, &msgSendPacket)
    s.Require().NoError(err)
    s.Require().NotEmpty(resp.TxHash)

    returnSendTxHash, err = hex.DecodeString(resp.TxHash)
    s.Require().NoError(err)

    s.Require().True(s.Run("Verify balances on Cosmos chain", func() {
			// User balance on Cosmos chain
			resp, err := e2esuite.GRPCQuery[banktypes.QueryBalanceResponse](ctx, simd, &banktypes.QueryBalanceRequest{
				Address: cosmosUserAddress,
				Denom:   ibcCoin.Denom,
			})
			s.Require().NoError(err)
			s.Require().NotNil(resp.Balance)
			s.Require().Equal(ibcCoin.Denom, resp.Balance.Denom)
			s.Require().Equal(testvalues.InitialBalance - totalTransferAmount.Int64(), resp.Balance.Amount.Int64())
		}))
  }))

	s.Require().True(s.Run("Receive packet on Ethereum", func() {
		var recvRelayTx []byte
		s.Require().True(s.Run("Retrieve relay tx", func() {
			resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
				SrcChain:    simd.Config().ChainID,
				DstChain:    eth.ChainID.String(),
				SourceTxIds: [][]byte{returnSendTxHash},
				SrcClientId: testvalues.FirstWasmClientID,
				DstClientId: testvalues.FirstUniversalClientID,
			})
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.Tx)
			s.Require().Equal(resp.Address, ics26Address.String())

			recvRelayTx = resp.Tx
		}))

		s.Require().True(s.Run("Submit relay tx", func() {
			receipt, err := eth.BroadcastTx(ctx, s.EthRelayerSubmitter, 15_000_000, &ics26Address, recvRelayTx)
			s.Require().NoError(err)
			s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status, fmt.Sprintf("Tx failed: %+v", receipt))
			s.T().Logf("Receive packet gas used: %d", receipt.GasUsed)

			_, err = e2esuite.GetEvmEvent(receipt, s.ics26Contract.ParseWriteAcknowledgement)
			s.Require().NoError(err)
		}))
		

    var escrowAddress ethcommon.Address
		s.True(s.Run("Verify balances on Ethereum", func() {
			// The transfer should be reflected on the user's balance
			userBalance, err := s.alloErc20Contract.BalanceOf(nil, ethereumUserAddress)
			s.Require().NoError(err)
			s.Require().Equal(totalTransferAmount, userBalance)

      // The total supply becomes the user's total balance (since it's first Cosmos -> Ethereum transfer which leads to tokens minting on Ethereum)
      totalSupply, err := s.alloErc20Contract.TotalSupply(nil)
      s.Require().NoError(err)
      s.Require().Equal(totalTransferAmount, totalSupply)

      escrowAddress, err = s.ics20Contract.GetEscrow(nil, testvalues.FirstWasmClientID)
			s.Require().NoError(err)

			escrowBalance, err := s.alloErc20Contract.BalanceOf(nil, escrowAddress)
			s.Require().NoError(err)
			s.Require().Zero(escrowBalance.Int64())
		}))

    s.Require().True(s.Run("Approve the ICS20Transfer.sol contract to spend the allo tokens", func() {
      tx, err := s.alloErc20Contract.Approve(s.GetTransactOpts(s.key, eth), ics20Address, totalTransferAmount)
      s.Require().NoError(err)
  
      receipt, err := eth.GetTxReciept(ctx, tx.Hash())
      s.Require().NoError(err)
      s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status)
  
      allowance, err := s.alloErc20Contract.Allowance(nil, ethereumUserAddress, ics20Address)
      s.Require().NoError(err)
      s.Require().Equal(totalTransferAmount, allowance)
    }))

    var (
      sendPacket ics26router.IICS26RouterMsgsPacket
      ethSendTxHash []byte
    )
    s.Require().True(s.Run("Send transfer on Ethereum", func() {
      timeout := uint64(time.Now().Add(30 * time.Minute).Unix())
  
      msgSendPacket := ics20transfer.IICS20TransferMsgsSendTransferMsg{
        Denom:            erc20Address,
        Amount:           totalTransferAmount,
        Receiver:         cosmosUserAddress,
        TimeoutTimestamp: timeout,
        SourceClient:     testvalues.FirstUniversalClientID,
        Memo:             "",
      }
  
      tx, err := s.ics20Contract.SendTransfer(s.GetTransactOpts(s.key, eth), msgSendPacket)
      s.Require().NoError(err)
      receipt, err := eth.GetTxReciept(ctx, tx.Hash())
      s.Require().NoError(err)
      s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status)
      ethSendTxHash = tx.Hash().Bytes()
  
      sendPacketEvent, err := e2esuite.GetEvmEvent(receipt, s.ics26Contract.ParseSendPacket)
      s.Require().NoError(err)
      sendPacket = sendPacketEvent.Packet
      s.Require().Equal(uint64(1), sendPacket.Sequence)
      s.Require().Equal(timeout, sendPacket.TimeoutTimestamp)
      s.Require().Len(sendPacket.Payloads, 1)
      s.Require().Equal(transfertypes.PortID, sendPacket.Payloads[0].SourcePort)
      s.Require().Equal(testvalues.FirstUniversalClientID, sendPacket.SourceClient)
      s.Require().Equal(transfertypes.PortID, sendPacket.Payloads[0].DestPort)
      s.Require().Equal(testvalues.FirstWasmClientID, sendPacket.DestClient)
      s.Require().Equal(transfertypes.V1, sendPacket.Payloads[0].Version)
      s.Require().Equal(transfertypes.EncodingABI, sendPacket.Payloads[0].Encoding)
  
      s.True(s.Run("Verify balances on Ethereum", func() {
        userBalance, err := s.alloErc20Contract.BalanceOf(nil, ethereumUserAddress)
        s.Require().NoError(err)
        s.Require().Zero(userBalance.Int64())
  
        // Escrow contract balance on Ethereum
        escrowBalance, err := s.alloErc20Contract.BalanceOf(nil, escrowAddress)
        s.Require().NoError(err)
        s.Require().Zero(escrowBalance.Int64())
      }))
    }))

    
    var (
      ackTxHash     []byte
    )
    s.Require().True(s.Run("Receive packets on Cosmos chain", func() {
      var relayTxBodyBz []byte
      s.Require().True(s.Run("Retrieve relay tx", func() {
        resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
          SrcChain:    eth.ChainID.String(),
          DstChain:    simd.Config().ChainID,
          SourceTxIds: [][]byte{ethSendTxHash},
          SrcClientId: testvalues.FirstUniversalClientID,
          DstClientId: testvalues.FirstWasmClientID,
        })
        s.Require().NoError(err)
        s.Require().NotEmpty(resp.Tx)
        s.Require().Empty(resp.Address)
  
        relayTxBodyBz = resp.Tx
      }))
  
      s.Require().True(s.Run("Broadcast relay tx", func() {
        resp := s.MustBroadcastSdkTxBody(ctx, simd, s.SimdRelayerSubmitter, 20_000_000, relayTxBodyBz)
  
        ackTxHash, err = hex.DecodeString(resp.TxHash)
        s.Require().NoError(err)
        s.Require().NotEmpty(ackTxHash)
      }))
  
      s.Require().True(s.Run("Verify balances on Cosmos chain", func() {
        // User balance on Cosmos chain
        resp, err := e2esuite.GRPCQuery[banktypes.QueryBalanceResponse](ctx, simd, &banktypes.QueryBalanceRequest{
          Address: cosmosUserAddress,
          Denom:   ibcCoin.Denom,
        })
        s.Require().NoError(err)
        s.Require().NotNil(resp.Balance)
        // The user balance should be back to the starting point
        s.Require().Equal(big.NewInt(testvalues.InitialBalance), resp.Balance.Amount.BigInt())
        s.Require().Equal(ibcCoin.Denom, resp.Balance.Denom)
      }))
    }))

    s.Require().True(s.Run("Acknowledge packets on Ethereum", func() {
      var ackRelayTx []byte
      s.Require().True(s.Run("Retrieve relay tx", func() {
        resp, err := s.RelayerClient.RelayByTx(context.Background(), &relayertypes.RelayByTxRequest{
          SrcChain:    simd.Config().ChainID,
          DstChain:    eth.ChainID.String(),
          SourceTxIds: [][]byte{ackTxHash},
          SrcClientId: testvalues.FirstWasmClientID,
          DstClientId: testvalues.FirstUniversalClientID,
        })
        s.Require().NoError(err)
        s.Require().NotEmpty(resp.Tx)
        s.Require().Equal(resp.Address, ics26Address.String())
  
        ackRelayTx = resp.Tx
      }))
  
      s.Require().True(s.Run("Submit relay tx", func() {
        receipt, err := eth.BroadcastTx(ctx, s.EthRelayerSubmitter, 15_000_000, &ics26Address, ackRelayTx)
        s.Require().NoError(err)
        s.Require().Equal(ethtypes.ReceiptStatusSuccessful, receipt.Status, fmt.Sprintf("Tx failed: %+v", receipt))
        s.T().Logf("Ack packet gas used: %d", receipt.GasUsed)
  
        // Verify the ack packet event exists
        _, err = e2esuite.GetEvmEvent(receipt, s.ics26Contract.ParseAckPacket)
        s.Require().NoError(err)
      }))

  
      s.Require().True(s.Run("Verify balances on Ethereum", func() {
        // User balance on Ethereum
        userBalance, err := s.alloErc20Contract.BalanceOf(nil, ethereumUserAddress)
        s.Require().NoError(err)
        s.Require().Zero(userBalance.Int64())
  
        // ICS20 contract balance on Ethereum
        escrowBalance, err := s.alloErc20Contract.BalanceOf(nil, escrowAddress)
        s.Require().NoError(err)
        s.Require().Zero(escrowBalance.Int64())
      }))
    }))
	}))
}