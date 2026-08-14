/**
 * Deploys the mock token and prints what the Go side needs.
 *
 * The address is written to deployed.json as well as printed, so a probe can
 * read it without scraping stdout.
 */
const fs = require('fs');
const path = require('path');
const hre = require('hardhat');

async function main() {
  const [deployer] = await hre.ethers.getSigners();

  const supply = hre.ethers.parseUnits('1000000', 18);
  const factory = await hre.ethers.getContractFactory('MockUSDT');
  const token = await factory.deploy(supply);
  await token.waitForDeployment();

  const address = (await token.getAddress()).toLowerCase();

  // The second deterministic account stands in for the service's receiving
  // address: fixed across runs, so a config file can name it directly.
  const receiver = (await hre.ethers.getSigners())[1];

  const out = {
    token: address,
    decimals: 18,
    symbol: 'USDT',
    deployer: (await deployer.getAddress()).toLowerCase(),
    payAddress: (await receiver.getAddress()).toLowerCase(),
  };

  fs.writeFileSync(path.join(__dirname, '..', 'deployed.json'), JSON.stringify(out, null, 2));

  console.log('token      ', out.token);
  console.log('pay address', out.payAddress);
  console.log('deployer   ', out.deployer);
}

main().catch((err) => {
  console.error(err);
  process.exitCode = 1;
});
