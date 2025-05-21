// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.28;

// solhint-disable custom-errors,gas-custom-errors

// solhint-disable-next-line no-global-import
import "forge-std/console.sol";

import {TransparentUpgradeableProxy} from "@openzeppelin-contracts/proxy/transparent/TransparentUpgradeableProxy.sol";
import {ProxyAdmin} from "@openzeppelin-contracts/proxy/transparent/ProxyAdmin.sol";
import {AlloOFTUpgradeable} from "@allora-oft-contracts/AlloOFTUpgradeable.sol";
import { TestAlloERC20} from "../../test/solidity-ibc/mocks/TestAlloERC20.sol";

contract MockLzEndpoint {
    function setDelegate(address _delegate) public {}
}

abstract contract DeployProxiedTestAlloERC20 {
    function deployProxiedTestAlloERC20(address _ics20Proxy) public returns (address) {
      address proxyOwner = address(msg.sender);
        address lzEndpoint = address(new MockLzEndpoint());

        ProxyAdmin proxyAdmin = new ProxyAdmin(proxyOwner);

        // Deploy implementation
        TestAlloERC20 implementation = new TestAlloERC20(lzEndpoint);
        console.log("Deployed TestAlloOFT implementation at address: ", address(implementation));

        // Encode the initialization data
        bytes memory initData = abi.encodeWithSelector(
            AlloOFTUpgradeable.initialize.selector,
            "Allora Network",
            "$ALLO",
            msg.sender,
            _ics20Proxy
        );

        // Deploy transparent upgradeable proxy
        TransparentUpgradeableProxy proxy = new TransparentUpgradeableProxy(
            address(implementation),
            address(proxyAdmin),
            initData
        );

        console.log("Deployed TestAlloERC20 at address: ", address(proxy));
        TestAlloERC20 alloOFT = TestAlloERC20(address(proxy));

        // Verify Initialization
        console.log("Token name: ", alloOFT.name());
        console.log("Token symbol: ", alloOFT.symbol());
        console.log("Token decimals: ", alloOFT.decimals());
        console.log("ICS20 Proxy address: ", alloOFT.ics20Proxy());
        console.log("Owner address: ", alloOFT.owner());

        require(keccak256(bytes(alloOFT.name())) == keccak256(bytes("Allora Network")), "Name mismatch");
        require(keccak256(bytes(alloOFT.symbol())) == keccak256(bytes("$ALLO")), "Symbol mismatch");
        require(alloOFT.decimals() == 18, "Decimals mismatch");
        require(alloOFT.ics20Proxy() == _ics20Proxy, "ICS20 Proxy mismatch");
        require(address(alloOFT.owner()) == proxyOwner, "Owner mismatch");
        require(address(alloOFT.endpoint()) == lzEndpoint, "LayerZero Endpoint mismatch");

        return address(proxy);
    }
}
