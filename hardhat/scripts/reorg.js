/**
 * Reorganises the chain: takes a snapshot, sends a transfer, then reverts.
 *
 * This is the whole reason the bench exists. No public testnet can be made to
 * un-mine a transaction on command, and `detected -> pending` is the branch that
 * handles a payment being withdrawn after the merchant has already been shown
 * it. Without this, that path ships untested.
 *
 * Usage:
 *   npx hardhat run scripts/reorg.js --network localhost           # snapshot + transfer, prints the id
 *   SNAPSHOT=0x1 npx hardhat run scripts/reorg.js --network localhost  # revert to it
 *
 * Between the two calls the watcher should see the transfer and move the invoice
 * to detected; after the revert it should see the log come back with
 * removed: true and hand the invoice back.
 */
const fs = require('fs');
const path = require('path');
const hre = require('hardhat');

async function main() {
  const snapshot = process.env.SNAPSHOT;

  if (snapshot) {
    const ok = await hre.network.provider.send('evm_revert', [snapshot]);
    const head = await hre.ethers.provider.getBlockNumber();
    console.log(`reverted to ${snapshot}: ${ok}, head is now ${head}`);
    return;
  }

  const id = await hre.network.provider.send('evm_snapshot', []);
  console.log('snapshot', id);

  const deployed = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'deployed.json'), 'utf8'));
  const amount = process.env.AMOUNT || '3.140000000000000001';
  const token = await hre.ethers.getContractAt('MockUSDT', deployed.token);
  const tx = await token.transfer(deployed.payAddress, hre.ethers.parseUnits(amount, deployed.decimals));
  const receipt = await tx.wait();

  console.log('tx     ', receipt.hash);
  console.log('block  ', receipt.blockNumber);
  console.log('amount ', hre.ethers.parseUnits(amount, deployed.decimals).toString(), 'units');
  console.log('');
  console.log(`to undo: SNAPSHOT=${id} npx hardhat run scripts/reorg.js --network localhost`);
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
