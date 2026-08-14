/**
 * Mines blocks on demand.
 *
 * Usage: BLOCKS=70 npx hardhat run scripts/mine.js --network localhost
 *
 * This is what makes the confirmation boundary testable: on a real chain the
 * watcher has to wait for finality, here it arrives when the test says so.
 */
const hre = require('hardhat');

async function main() {
  const blocks = Number(process.env.BLOCKS || 1);
  await hre.network.provider.send('hardhat_mine', ['0x' + blocks.toString(16)]);
  const head = await hre.ethers.provider.getBlockNumber();
  console.log(`mined ${blocks}, head is now ${head}`);
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
