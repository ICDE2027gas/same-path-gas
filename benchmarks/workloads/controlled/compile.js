/* Bind standard-JSON compilation to the recorded compiler version. Imports are disabled. */
'use strict';
const fs = require('fs');
const crypto = require('crypto');
const assert = require('assert');
assert.strictEqual(process.version, 'v12.22.12');
const [compilerPath, inputPath, outputPath, infoPath] = process.argv.slice(2);
assert(compilerPath && inputPath && outputPath && infoPath);
const compilerBytes = fs.readFileSync(compilerPath);
assert.strictEqual(crypto.createHash('sha256').update(compilerBytes).digest('hex'), '52ad76de0634e875ac3d50e09a0057532ff53c6d851ef61204af1b0d620797b6');
const soljson = require(compilerPath);
const version = soljson.cwrap('solidity_version', 'string', [])();
assert.strictEqual(version, '0.5.16+commit.9c3226ce.Emscripten.clang');
const inputRaw = fs.readFileSync(inputPath, 'utf8');
const input = JSON.parse(inputRaw);
assert.strictEqual(input.language, 'Solidity');
assert.deepStrictEqual(Object.keys(input.sources), ['BenignBatch.sol']);
assert.strictEqual(input.settings.evmVersion, 'istanbul');
assert.deepStrictEqual(input.settings.optimizer, {enabled: true, runs: 999999});
const add = soljson.addFunction || soljson.Runtime.addFunction;
const remove = soljson.removeFunction || soljson.Runtime.removeFunction;
const callback = add(function (_path, _contents, error) {
    const message = 'Imports disabled: source is complete';
    const pointer = soljson._malloc(soljson.lengthBytesUTF8(message) + 1);
    soljson.stringToUTF8(message, pointer, soljson.lengthBytesUTF8(message) + 1);
    soljson.setValue(error, pointer, '*');
});
let output;
try { output = soljson.cwrap('solidity_compile', 'string', ['string', 'number'])(inputRaw, callback); }
finally { remove(callback); }
fs.writeFileSync(outputPath, output, {flag: 'wx'});
const parsed = JSON.parse(output);
fs.writeFileSync(infoPath, JSON.stringify({version, node: process.version, errors: parsed.errors || []}, null, 2) + '\n', {flag: 'wx'});
assert(!(parsed.errors || []).some(row => row.severity === 'error'), 'Compilation failed; complete output retained');
for (const name of ['BenignBatch', 'BenignForwarder']) {
    const contract = parsed.contracts['BenignBatch.sol'][name];
    assert(contract && contract.evm.bytecode.object && contract.evm.deployedBytecode.object);
    assert(contract.evm.bytecode.object.length / 2 <= 16384 && contract.evm.deployedBytecode.object.length / 2 <= 8192);
    assert.strictEqual(JSON.parse(contract.metadata).compiler.version, '0.5.16+commit.9c3226ce');
}
