#!/usr/bin/env python3
"""Compile the controlled contracts with the pinned, caller-supplied compiler."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
SOLJSON_SHA = '52ad76de0634e875ac3d50e09a0057532ff53c6d851ef61204af1b0d620797b6'
TOKEN_SHA = '7917faf747fc7f3023bfe102bde224ecce2f261c5993b0a5f2308f95789ff530'


def spec(path):
    path = path.resolve(strict=True)
    raw = path.read_bytes()
    return {'path': str(path), 'bytes': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()}


def save(path, value):
    with path.open('x', encoding='utf-8', newline='\n') as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write('\n')
    return spec(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--node', required=True, type=Path, help='Node v12.22.12 executable')
    parser.add_argument('--soljson', required=True, type=Path, help='Pinned soljson-v0.5.16+commit.9c3226ce.js')
    parser.add_argument('--out', required=True, type=Path, help='Fresh output directory')
    args = parser.parse_args()
    node, compiler = args.node.resolve(strict=True), args.soljson.resolve(strict=True)
    if spec(compiler)['sha256'] != SOLJSON_SHA:
        raise ValueError('Compiler SHA256 differs from COMPILER_PIN.json')
    token_path = HERE / 'ERC20.original.json'
    if spec(token_path) != {'path': str(token_path.resolve()), 'bytes': 69315, 'sha256': TOKEN_SHA}:
        raise ValueError('Original public ERC20 artifact identity differs')
    out = args.out.absolute()
    out.parent.resolve(strict=True)
    out.mkdir(mode=0o700)  # Existing output directories are never reused.
    out = out.resolve(strict=True)
    protected = {name: spec(HERE / name) for name in ('BenignBatch.sol', 'SCHEDULE.json', 'compile.js', 'ERC20.original.json')}
    protected.update({'node': spec(node), 'compiler': spec(compiler)})
    receipt = {'schema': 'gas-certificate-portable-compiler-receipt-v1', 'complete': False, 'inputs': protected,
               'compiler_invocations': 0, 'Go_EVM_or_application_invocations': 0, 'errors': []}
    try:
        standard = {'language': 'Solidity', 'sources': {'BenignBatch.sol': {'content': (HERE / 'BenignBatch.sol').read_bytes().decode('utf-8')}},
                    'settings': {'optimizer': {'enabled': True, 'runs': 999999}, 'evmVersion': 'istanbul',
                                 'outputSelection': {'*': {'*': ['abi', 'evm.bytecode.object', 'evm.deployedBytecode.object', 'metadata']}}}}
        input_spec = save(out / 'compiler-input.json', standard)
        env = os.environ.copy()
        env.pop('NODE_OPTIONS', None)
        env['NODE_PATH'] = ''
        receipt['compiler_invocations'] = 1
        try:
            process = subprocess.run([str(node), str(HERE / 'compile.js'), str(compiler), input_spec['path'],
                                      str(out / 'compiler-output.json'), str(out / 'compiler-info.json')],
                                     capture_output=True, env=env, timeout=240)
        except subprocess.TimeoutExpired as error:
            (out / 'compile.stdout').write_bytes(error.stdout or b'')
            (out / 'compile.stderr').write_bytes(error.stderr or b'')
            raise
        (out / 'compile.stdout').write_bytes(process.stdout)
        (out / 'compile.stderr').write_bytes(process.stderr)
        receipt['compiler_returncode'] = process.returncode
        if process.returncode:
            raise RuntimeError('Compilation failed; complete compiler output retained')
        compiled = json.loads((out / 'compiler-output.json').read_bytes())
        info = json.loads((out / 'compiler-info.json').read_bytes())
        if info['version'] != '0.5.16+commit.9c3226ce.Emscripten.clang' or info['node'] != 'v12.22.12':
            raise ValueError('Compiler/runtime version differs')
        sources = {'solidity_source': protected['BenignBatch.sol'], 'compile_script': protected['compile.js'],
                   'schedule': protected['SCHEDULE.json'], 'erc20_artifact': protected['ERC20.original.json'],
                   'standard_input': input_spec, 'compiler-output.json': spec(out / 'compiler-output.json'),
                   'compiler-info.json': spec(out / 'compiler-info.json')}
        contracts = {}
        for name in ('BenignBatch', 'BenignForwarder'):
            contract = compiled['contracts']['BenignBatch.sol'][name]
            contracts[name] = {'abi': contract['abi'], 'bytecode': '0x' + contract['evm']['bytecode']['object'],
                               'runtime': '0x' + contract['evm']['deployedBytecode']['object'], 'metadata': contract['metadata']}
        token = json.loads(token_path.read_bytes())
        artifacts = {'schema': 'round1832-compiled-contracts-v1', 'complete': True, 'compiler': spec(compiler), 'node': spec(node),
                     'sources': sources, 'contracts': contracts,
                     'erc20': {'abi': token['abi'], 'bytecode': token['bytecode'], 'runtime': '0x' + token['evm']['deployedBytecode']['object']},
                     'compile_errors_and_warnings': info['errors']}
        receipt['artifacts'] = save(out / 'artifacts.json', artifacts)
        receipt['complete'] = True
    except Exception as error:
        receipt['errors'].append(type(error).__name__ + ': ' + str(error))
        raise
    finally:
        receipt['inputs_preserved'] = all(spec(Path(value['path'])) == value for value in protected.values())
        receipt['complete'] = receipt['complete'] and receipt['inputs_preserved']
        save(out / 'compiler-receipt.json', receipt)
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
