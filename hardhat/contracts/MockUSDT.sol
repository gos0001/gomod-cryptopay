// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title A minimal ERC20 standing in for BEP-20 USDT.
///
/// Hand-written rather than pulled from OpenZeppelin: the watcher only ever
/// reads the Transfer event, and the whole point of the bench is to confirm that
/// the event this contract emits is laid out the way pkg/evm assumes — indexed
/// from, indexed to, unindexed value. A dependency would put someone else's
/// implementation between the assumption and the check.
///
/// 18 decimals on purpose: that is BEP-20 USDT, and it is the scale where an
/// amount stops fitting in a float64 or an int64.
contract MockUSDT {
    string public name = "Mock Tether USD";
    string public symbol = "USDT";
    uint8 public decimals = 18;
    uint256 public totalSupply;

    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

    constructor(uint256 initialSupply) {
        totalSupply = initialSupply;
        balanceOf[msg.sender] = initialSupply;
        emit Transfer(address(0), msg.sender, initialSupply);
    }

    function transfer(address to, uint256 value) external returns (bool) {
        _transfer(msg.sender, to, value);
        return true;
    }

    function approve(address spender, uint256 value) external returns (bool) {
        allowance[msg.sender][spender] = value;
        emit Approval(msg.sender, spender, value);
        return true;
    }

    function transferFrom(address from, address to, uint256 value) external returns (bool) {
        uint256 allowed = allowance[from][msg.sender];
        require(allowed >= value, "allowance");
        allowance[from][msg.sender] = allowed - value;
        _transfer(from, to, value);
        return true;
    }

    /// @dev Also emits an Approval event on the same address in one test path, so
    /// the watcher's topic filter has a non-Transfer event to ignore.
    function _transfer(address from, address to, uint256 value) private {
        require(balanceOf[from] >= value, "balance");
        balanceOf[from] -= value;
        balanceOf[to] += value;
        emit Transfer(from, to, value);
    }
}
