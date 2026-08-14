/**
 * Sends one token transfer to the receiving address.
 *
 * Usage: AMOUNT=7.250000000000000001 npx hardhat run scripts/transfer.js --network localhost
 *
 * The default amount carries a non-zero digit in the last of its eighteen
 * decimal places on purpose: that digit is what a float64 would quietly drop,
 * and confirming it survives the contract, the log and NUMERIC(78,0) is the
 * point of sending it.
 */
const fs = require('fs');
const path = require('path');
const hre = require('hardhat');

async function main() {
  const deployed = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'deployed.json'), 'utf8'));

  const amount = process.env.AMOUNT || '7.250000000000000001';
  const to = process.env.TO || deployed.payAddress;

  const token = await hre.ethers.getContractAt('MockUSDT', deployed.token);
  const tx = await token.transfer(to, hre.ethers.parseUnits(amount, deployed.decimals));
  const receipt = await tx.wait();

  console.log('tx     ', receipt.hash);
  console.log('block  ', receipt.blockNumber);
  console.log('to     ', to);
  console.log('amount ', hre.ethers.parseUnits(amount, deployed.decimals).toString(), 'units');
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
