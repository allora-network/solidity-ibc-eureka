// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.28;

// solhint-disable custom-errors,gas-custom-errors

// solhint-disable-next-line no-global-import
import "forge-std/console.sol";

import {TransparentUpgradeableProxy} from "@openzeppelin-contracts/proxy/transparent/TransparentUpgradeableProxy.sol";
import {ProxyAdmin} from "@openzeppelin-contracts/proxy/transparent/ProxyAdmin.sol";
import {AlloOFTUpgradeable} from "@allora-oft-contracts/AlloOFTUpgradeable.sol";

contract TestAlloERC20 is AlloOFTUpgradeable {
    constructor(address _lzEndpoint) AlloOFTUpgradeable(_lzEndpoint) {}
    function forceMint(address _to, uint256 _amount) public {
        _mint(_to, _amount);
    }
}