/**
 * Local EVM node for developing the BSC watcher.
 *
 * The point of a local node is not convenience: it is that a public testnet
 * cannot be made to reorganise on demand, and the watcher's detected -> pending
 * path is exactly the branch that handles a payment being un-mined. Here that is
 * evm_snapshot and evm_revert.
 *
 * The chain id is BSC's so that anything reading it sees what it will see in
 * production. Block time is left at "mine on transaction" rather than an
 * interval: tests decide when blocks appear, via hardhat_mine.
 */
require('@nomicfoundation/hardhat-ethers');

module.exports = {
  solidity: {
    version: '0.8.24',
    settings: { optimizer: { enabled: false } },
  },
  networks: {
    hardhat: {
      chainId: 56,
      // Deterministic accounts: the same addresses every run, so a config file
      // can name the receiving address without a setup step to discover it.
      accounts: { mnemonic: 'test test test test test test test test test test test junk' },
    },
    localhost: {
      url: 'http://127.0.0.1:8545',
      chainId: 56,
    },
  },
  paths: {
    sources: './contracts',
    artifacts: './artifacts',
    cache: './cache',
  },
};
