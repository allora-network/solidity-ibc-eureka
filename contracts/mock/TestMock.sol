// SPDX-License-Identifier: MIT
pragma solidity ^0.8.22;

import {AlloOFTUpgradeable} from "@allora-oft-contracts/AlloOFTUpgradeable.sol";

// Workaround to add AlloOFTUpgradeable to the out/ directory
// this is needed during the E2E test deployment
contract TestMock {
    AlloOFTUpgradeable public alloErc20;

    constructor(address _alloErc20) {
        alloErc20 = AlloOFTUpgradeable(_alloErc20);
    }

    function test() public pure returns (bool) {
        return true;
    }
}